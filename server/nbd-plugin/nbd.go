// Package nbd implements the minimum fixed-newstyle NBD protocol required for
// WiiBridge exports. Wii backends remain immutable; a selected GameCube
// profile may opt into writes solely for Nintendont memory-card emulation.
// Every export requires NBD_OPT_STARTTLS before selection.
package nbd

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"wiibridge/shared/perf"
)

const (
	nbdMagic       = uint64(0x4e42444d41474943)
	optMagic       = uint64(0x49484156454f5054)
	repMagic       = uint64(0x0003e889045565a9)
	requestMagic   = uint32(0x25609513)
	replyMagic     = uint32(0x67446698)
	optExportName  = uint32(1)
	optAbort       = uint32(2)
	optStartTLS    = uint32(5)
	optInfo        = uint32(6)
	optGo          = uint32(7)
	repAck         = uint32(1)
	repInfo        = uint32(3)
	repErrUnsup    = uint32(0x80000001)
	cmdRead        = uint16(0)
	cmdWrite       = uint16(1)
	cmdDisconnect  = uint16(2)
	cmdFlush       = uint16(3)
	cmdTrim        = uint16(4)
	cmdWriteZeroes = uint16(6)
	flagFixedNew   = uint16(1)
	flagNoZeroes   = uint16(2)
	flagHasFlags   = uint16(1)
	flagReadOnly   = uint16(2)
	flagSendFlush  = uint16(4)
	errPerm        = uint32(1)
	errIO          = uint32(5)
	errInvalid     = uint32(22)
)

var errObservedRead = errors.New("NBD read failed")

type Backend interface {
	Size() int64
	ReadAt([]byte, int64) (int, error)
}

type writeBackend interface {
	WriteAt([]byte, int64) (int, error)
	Sync() error
}

type modeBackend interface {
	ReadOnly() bool
}

type Server struct {
	Backend Backend // fixed backend, primarily for tests and single snapshots
	// BackendProvider is called once per connection.  The returned immutable
	// backend remains pinned for the complete NBD session.
	BackendProvider func() Backend
	// BackendAcquirer pins a profile backend for the complete session and
	// returns a release callback. It is used by the platform export manager to
	// prevent switching while requests may still be outstanding.
	BackendAcquirer func() (Backend, func(), error)
	TLS             *tls.Config
	ExportName      string
	Deadline        time.Duration
	MaxRequest      uint32
	Logger          *slog.Logger
	Metrics         *perf.Registry
	mu              sync.Mutex
	active          map[net.Conn]struct{}
	requestBuffers  sync.Pool
	accepted        atomic.Uint64
}

type requestBuffer struct {
	frame []byte
	data  []byte
}

const replyHeaderSize = 16

func (s *Server) metricsEnabled() bool {
	return s.Metrics != nil && s.Metrics.Enabled()
}

func (s *Server) acquireRequestBuffer(length int) *requestBuffer {
	buffer, _ := s.requestBuffers.Get().(*requestBuffer)
	if buffer == nil {
		buffer = &requestBuffer{}
	}
	// Reserve the simple-reply header ahead of the payload so TLS can frame
	// one complete reply without another allocation or payload copy.
	frameLength := replyHeaderSize + length
	if cap(buffer.frame) < frameLength {
		buffer.frame = make([]byte, frameLength)
	} else {
		buffer.frame = buffer.frame[:frameLength]
	}
	buffer.data = buffer.frame[replyHeaderSize:]
	return buffer
}

func (s *Server) releaseRequestBuffer(buffer *requestBuffer) {
	if buffer == nil {
		return
	}
	if cap(buffer.data) <= int(s.MaxRequest) {
		s.requestBuffers.Put(buffer)
	}
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if (s.Backend == nil && s.BackendProvider == nil && s.BackendAcquirer == nil) ||
		s.TLS == nil || s.TLS.ClientAuth != tls.RequireAndVerifyClientCert {
		return errors.New("backend and mutual TLS are required")
	}
	if s.Deadline == 0 {
		s.Deadline = 30 * time.Second
	}
	if s.MaxRequest == 0 {
		s.MaxRequest = 1 << 20
	}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	s.active = make(map[net.Conn]struct{})
	go func() {
		<-ctx.Done()
		listener.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		for c := range s.active {
			c.Close()
		}
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.mu.Lock()
		s.active[conn] = struct{}{}
		s.mu.Unlock()
		if s.metricsEnabled() {
			s.Metrics.NBD.ActiveConnections.Add(1)
			if s.accepted.Add(1) > 1 {
				s.Metrics.NBD.Reconnects.Add(1)
			}
		}
		go func() {
			defer func() {
				conn.Close()
				s.mu.Lock()
				delete(s.active, conn)
				s.mu.Unlock()
				if s.metricsEnabled() {
					s.Metrics.NBD.ActiveConnections.Add(-1)
					s.Metrics.RecordNBDDisconnect()
				}
			}()
			if err := s.handle(conn); err != nil && !errors.Is(err, io.EOF) {
				if s.metricsEnabled() {
					var networkError net.Error
					if errors.As(err, &networkError) && networkError.Timeout() {
						s.Metrics.NBD.Timeouts.Add(1)
					}
				}
				s.Logger.Warn("NBD session closed", "error", err)
			}
		}()
	}
}

func (s *Server) handle(raw net.Conn) (returnErr error) {
	negotiated := false
	defer func() {
		if returnErr != nil && !negotiated && s.metricsEnabled() {
			s.Metrics.NBD.NegotiationFailure.Add(1)
		}
	}()
	backend := s.Backend
	release := func() {}
	if s.BackendAcquirer != nil {
		var err error
		backend, release, err = s.BackendAcquirer()
		if err != nil {
			return err
		}
		if release == nil {
			return errors.New("backend acquirer returned no release callback")
		}
		defer release()
	}
	if s.BackendProvider != nil {
		backend = s.BackendProvider()
	}
	if backend == nil {
		return errors.New("no published backend")
	}
	readOnly := true
	if mode, ok := backend.(modeBackend); ok {
		readOnly = mode.ReadOnly()
	}
	if !readOnly {
		if _, ok := backend.(writeBackend); !ok {
			return errors.New("writable backend lacks write and sync support")
		}
	}
	transmissionFlags := uint16(flagHasFlags | flagSendFlush)
	if readOnly {
		transmissionFlags |= flagReadOnly
	}
	raw.SetDeadline(time.Now().Add(s.Deadline))
	if err := binary.Write(raw, binary.BigEndian, nbdMagic); err != nil {
		return err
	}
	if err := binary.Write(raw, binary.BigEndian, optMagic); err != nil {
		return err
	}
	if err := binary.Write(raw, binary.BigEndian, flagFixedNew|flagNoZeroes); err != nil {
		return err
	}
	var clientFlags uint32
	if err := binary.Read(raw, binary.BigEndian, &clientFlags); err != nil {
		return err
	}
	if clientFlags&1 == 0 {
		return errors.New("client did not request fixed-newstyle")
	}
	var conn net.Conn = raw
	secured := false
	for {
		var magic uint64
		var option, length uint32
		if err := binary.Read(conn, binary.BigEndian, &magic); err != nil {
			return err
		}
		if magic != optMagic {
			return errors.New("invalid option magic")
		}
		if err := binary.Read(conn, binary.BigEndian, &option); err != nil {
			return err
		}
		if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
			return err
		}
		if length > 64<<10 {
			return errors.New("option too large")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return err
		}
		if !secured {
			if option != optStartTLS || length != 0 {
				if err := writeOptionReply(conn, option, repErrUnsup, nil); err != nil {
					return err
				}
				continue
			}
			if err := writeOptionReply(conn, option, repAck, nil); err != nil {
				return err
			}
			tlsConn := tls.Server(raw, s.TLS.Clone())
			tlsStarted := time.Now()
			if err := tlsConn.Handshake(); err != nil {
				if s.metricsEnabled() {
					s.Metrics.NBD.TLSFailures.Add(1)
					s.Metrics.NBD.TLSLatency.Observe(time.Since(tlsStarted))
				}
				return err
			}
			if s.metricsEnabled() {
				s.Metrics.NBD.TLSLatency.Observe(time.Since(tlsStarted))
			}
			conn, secured = tlsConn, true
			continue
		}
		switch option {
		case optAbort:
			_ = writeOptionReply(conn, option, repAck, nil)
			return nil
		case optExportName:
			if string(payload) != s.ExportName {
				return errors.New("unknown export")
			}
			if err := binary.Write(conn, binary.BigEndian, uint64(backend.Size())); err != nil {
				return err
			}
			if err := binary.Write(conn, binary.BigEndian, transmissionFlags); err != nil {
				return err
			}
			negotiated = true
			return s.transmission(conn, backend, readOnly)
		case optInfo, optGo:
			name, ok := parseInfoName(payload)
			if !ok || name != s.ExportName {
				if err := writeOptionReply(conn, option, repErrUnsup, nil); err != nil {
					return err
				}
				continue
			}
			info := make([]byte, 12)
			binary.BigEndian.PutUint16(info[0:2], 0)
			binary.BigEndian.PutUint64(info[2:10], uint64(backend.Size()))
			binary.BigEndian.PutUint16(info[10:12], transmissionFlags)
			if err := writeOptionReply(conn, option, repInfo, info); err != nil {
				return err
			}
			block := make([]byte, 14)
			binary.BigEndian.PutUint16(block[0:2], 3)
			binary.BigEndian.PutUint32(block[2:6], 512)
			binary.BigEndian.PutUint32(block[6:10], 64<<10)
			binary.BigEndian.PutUint32(block[10:14], s.MaxRequest)
			if err := writeOptionReply(conn, option, repInfo, block); err != nil {
				return err
			}
			if err := writeOptionReply(conn, option, repAck, nil); err != nil {
				return err
			}
			if option == optGo {
				negotiated = true
				return s.transmission(conn, backend, readOnly)
			}
		default:
			if err := writeOptionReply(conn, option, repErrUnsup, nil); err != nil {
				return err
			}
		}
	}
}

func parseInfoName(payload []byte) (string, bool) {
	if len(payload) < 4 {
		return "", false
	}
	n := int(binary.BigEndian.Uint32(payload[:4]))
	if n < 0 || len(payload) < 4+n+2 {
		return "", false
	}
	return string(payload[4 : 4+n]), true
}

func writeOptionReply(w io.Writer, option, reply uint32, payload []byte) error {
	var frame bytes.Buffer
	for _, v := range []any{repMagic, option, reply, uint32(len(payload))} {
		if err := binary.Write(&frame, binary.BigEndian, v); err != nil {
			return err
		}
	}
	frame.Write(payload)
	_, err := w.Write(frame.Bytes())
	return err
}

func (s *Server) transmission(conn net.Conn, backend Backend, readOnly bool) error {
	// A connected block device may legitimately be idle for an arbitrary
	// period. The negotiation deadline protects the unauthenticated and TLS
	// setup phases, but carrying it into transmission tears down healthy
	// mounted devices when no read arrives before the deadline.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	var requestHeader [28]byte
	var responseHeader [replyHeaderSize]byte
	for {
		if _, err := io.ReadFull(conn, requestHeader[:]); err != nil {
			return err
		}
		magic := binary.BigEndian.Uint32(requestHeader[0:4])
		flagsType := binary.BigEndian.Uint32(requestHeader[4:8])
		handle := binary.BigEndian.Uint64(requestHeader[8:16])
		offset := binary.BigEndian.Uint64(requestHeader[16:24])
		length := binary.BigEndian.Uint32(requestHeader[24:28])
		if magic != requestMagic {
			if s.metricsEnabled() {
				s.Metrics.NBD.ProtocolErrors.Add(1)
			}
			return errors.New("invalid request magic")
		}
		command := uint16(flagsType)
		if command == cmdDisconnect {
			return nil
		}
		status := uint32(0)
		var data []byte
		var readBuffer *requestBuffer
		requestStarted := time.Time{}
		if s.metricsEnabled() {
			requestStarted = time.Now()
			depth := s.Metrics.NBD.QueueDepth.Add(1)
			for {
				maximum := s.Metrics.NBD.MaxQueueDepth.Load()
				if depth <= maximum ||
					s.Metrics.NBD.MaxQueueDepth.CompareAndSwap(maximum, depth) {
					break
				}
			}
		}
		switch command {
		case cmdRead:
			if length == 0 || length > s.MaxRequest || offset > uint64(backend.Size()) ||
				uint64(length) > uint64(backend.Size())-offset {
				status = errInvalid
			} else {
				readBuffer = s.acquireRequestBuffer(int(length))
				data = readBuffer.data
				if n, err := backend.ReadAt(data, int64(offset)); err != nil || n != len(data) {
					s.releaseRequestBuffer(readBuffer)
					readBuffer = nil
					status, data = errIO, nil
				}
			}
		case cmdFlush:
			if !readOnly {
				if err := backend.(writeBackend).Sync(); err != nil {
					status = errIO
				}
			}
		case cmdWrite:
			if s.metricsEnabled() {
				s.Metrics.NBD.WriteRequests.Add(1)
			}
			if length == 0 || length > s.MaxRequest || offset > uint64(backend.Size()) ||
				uint64(length) > uint64(backend.Size())-offset {
				status = errInvalid
				if length <= s.MaxRequest {
					if _, err := io.CopyN(io.Discard, conn, int64(length)); err != nil {
						if s.metricsEnabled() {
							s.Metrics.NBD.QueueDepth.Add(-1)
						}
						return err
					}
				}
				break
			}
			readBuffer = s.acquireRequestBuffer(int(length))
			data = readBuffer.data
			if _, err := io.ReadFull(conn, data); err != nil {
				s.releaseRequestBuffer(readBuffer)
				readBuffer = nil
				if s.metricsEnabled() {
					s.Metrics.NBD.QueueDepth.Add(-1)
				}
				return err
			}
			if readOnly {
				status = errPerm
				if s.metricsEnabled() {
					s.Metrics.NBD.RejectedWrites.Add(1)
				}
			} else {
				n, err := backend.(writeBackend).WriteAt(data, int64(offset))
				if err != nil || n != len(data) {
					status = errIO
					var coded interface{ ErrorCode() string }
					if errors.As(err, &coded) {
						switch coded.ErrorCode() {
						case "SAVE-WRITE-OUTSIDE-EXTENT",
							"SAVE-WRITE-CROSSES-BOUNDARY":
							status = errPerm
						}
					}
					if s.metricsEnabled() {
						s.Metrics.NBD.RejectedWrites.Add(1)
					}
				}
			}
		case cmdTrim, cmdWriteZeroes:
			// Never translate discard or zero-range requests into host file
			// operations. Nintendont does not require them, and rejecting them
			// avoids destructive sparse-file surprises.
			status = errPerm
			if s.metricsEnabled() {
				s.Metrics.NBD.RejectedWrites.Add(1)
			}
			if command == cmdWriteZeroes && length > s.MaxRequest {
				status = errInvalid
			}
			if command == cmdWriteZeroes && length <= s.MaxRequest {
				// WRITE_ZEROES carries no request payload.
			}
			if command == cmdTrim {
				// TRIM carries no request payload.
			}
		default:
			status = errInvalid
			if s.metricsEnabled() {
				s.Metrics.NBD.ProtocolErrors.Add(1)
			}
		}
		if command == cmdWrite && length > s.MaxRequest {
			// Oversized payloads cannot be safely resynchronized; close the
			// session after returning no reply.
			if s.metricsEnabled() {
				s.Metrics.NBD.QueueDepth.Add(-1)
			}
			return errors.New("oversized write request")
		}
		frame := responseHeader[:]
		if status == 0 && command == cmdRead {
			frame = readBuffer.frame
		}
		binary.BigEndian.PutUint32(frame[0:4], replyMagic)
		binary.BigEndian.PutUint32(frame[4:8], status)
		binary.BigEndian.PutUint64(frame[8:16], handle)
		written, err := conn.Write(frame)
		if err != nil || written != len(frame) {
			s.releaseRequestBuffer(readBuffer)
			if s.metricsEnabled() {
				if command == cmdRead {
					payloadWritten := min(len(data), max(0, written-replyHeaderSize))
					s.Metrics.ObserveNBDRead(
						payloadWritten, time.Since(requestStarted), errObservedRead)
				}
				s.Metrics.NBD.QueueDepth.Add(-1)
			}
			if err == nil {
				err = io.ErrShortWrite
			}
			return err
		}
		s.releaseRequestBuffer(readBuffer)
		if s.metricsEnabled() {
			if command == cmdRead {
				var observedErr error
				if status != 0 {
					observedErr = errObservedRead
				}
				s.Metrics.ObserveNBDRead(
					len(data), time.Since(requestStarted), observedErr)
			} else {
				s.Metrics.NBD.RequestLatency.Observe(time.Since(requestStarted))
			}
			s.Metrics.NBD.QueueDepth.Add(-1)
		}
	}
}
