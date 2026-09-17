package nbd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"wiibridge/shared/perf"
)

func readFrame(handle, offset uint64, length uint32) []byte {
	frame := make([]byte, 28)
	binary.BigEndian.PutUint32(frame[0:4], requestMagic)
	binary.BigEndian.PutUint32(frame[4:8], uint32(cmdRead))
	binary.BigEndian.PutUint64(frame[8:16], handle)
	binary.BigEndian.PutUint64(frame[16:24], offset)
	binary.BigEndian.PutUint32(frame[24:28], length)
	return frame
}

type shortReadBackend struct{ reads int }

func (*shortReadBackend) Size() int64 { return 4096 }
func (b *shortReadBackend) ReadAt(data []byte, offset int64) (int, error) {
	b.reads++
	if offset == 1 {
		data[0] = 0x99
		return 1, nil
	}
	for i := range data {
		data[i] = byte(offset + int64(i))
	}
	return len(data), nil
}

func TestReadRepliesKeepHandlesLengthsAndRejectShortBackendReads(t *testing.T) {
	server := &Server{MaxRequest: 512, Metrics: perf.New(perf.Config{Enabled: true})}
	backend := &shortReadBackend{}
	input := append(readFrame(11, 0, 512), readFrame(22, 1, 512)...)
	input = append(input, readFrame(33, 128, 32)...)
	conn := &benchmarkConn{}
	conn.Reset(input)
	if err := server.transmission(conn, backend, true); err != io.EOF {
		t.Fatal(err)
	}
	output := conn.output.Bytes()
	for _, expected := range []struct {
		handle uint64
		status uint32
		offset int
		length int
	}{{11, 0, 0, 512}, {22, errIO, 0, 0}, {33, 0, 128, 32}} {
		if len(output) < 16+expected.length {
			t.Fatal("truncated response")
		}
		if binary.BigEndian.Uint32(output[:4]) != replyMagic ||
			binary.BigEndian.Uint32(output[4:8]) != expected.status ||
			binary.BigEndian.Uint64(output[8:16]) != expected.handle {
			t.Fatalf("unexpected response header: %x", output[:16])
		}
		for i, value := range output[16 : 16+expected.length] {
			if value != byte(expected.offset+i) {
				t.Fatal("response contains stale or incorrect bytes")
			}
		}
		output = output[16+expected.length:]
	}
	if len(output) != 0 {
		t.Fatal("response included unused pooled capacity")
	}
	if server.Metrics.NBD.QueueDepth.Load() != 0 || server.Metrics.NBD.BytesSent.Load() != 544 {
		t.Fatal("short backend read corrupted byte or queue accounting")
	}
}

type limitedReplyConn struct {
	benchmarkConn
	limit int
	err   error
}

func (c *limitedReplyConn) Write(data []byte) (int, error) {
	n := min(c.limit, len(data))
	_, _ = c.output.Write(data[:n])
	return n, c.err
}

func TestIncompleteReplyWritesReturnErrorsAndAccountOnlyPayload(t *testing.T) {
	for _, test := range []struct {
		name  string
		limit int
		err   error
	}{{"short header", 7, nil}, {"short payload", 23, nil}, {"network failure", 29, io.ErrClosedPipe}} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{MaxRequest: 512, Metrics: perf.New(perf.Config{Enabled: true})}
			server.Metrics.StartSession("wii", "", "test", "test", 1)
			conn := &limitedReplyConn{limit: test.limit, err: test.err}
			conn.Reset(readFrame(71, 0, 100))
			err := server.transmission(conn, &shortReadBackend{}, true)
			want := test.err
			if want == nil {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			if got := server.Metrics.NBD.BytesSent.Load(); got != 0 {
				t.Fatalf("failed read counted as successfully sent: %d", got)
			}
			session, ok := server.Metrics.CurrentSession()
			if !ok || session.TotalBytes != uint64(max(0, test.limit-16)) {
				t.Fatalf("partial session payload accounting: %+v", session)
			}
			if server.Metrics.NBD.QueueDepth.Load() != 0 {
				t.Fatal("failed reply retained an active request")
			}
		})
	}
}

func TestTruncatedRequestHeadersNeverReachTheBackend(t *testing.T) {
	frame := readFrame(1, 0, 32)
	for length := 0; length < len(frame); length++ {
		backend := &shortReadBackend{}
		conn := &benchmarkConn{}
		conn.Reset(frame[:length])
		server := &Server{MaxRequest: 512}
		err := server.transmission(conn, backend, true)
		if err == nil || backend.reads != 0 || conn.output.Len() != 0 {
			t.Fatalf("truncated request length %d was dispatched", length)
		}
	}
}

func TestSuccessfulReadUsesOneCompleteReplyWrite(t *testing.T) {
	conn := &countReplyConn{}
	conn.Reset(readFrame(1, 0, 512))
	server := &Server{MaxRequest: 512}
	if err := server.transmission(conn, &shortReadBackend{}, true); err != io.EOF {
		t.Fatal(err)
	}
	if conn.writes != 1 || conn.output.Len() != 528 {
		t.Fatalf("reply writes=%d bytes=%d", conn.writes, conn.output.Len())
	}
	buffer := server.acquireRequestBuffer(32)
	defer server.releaseRequestBuffer(buffer)
	if cap(buffer.frame) > int(server.MaxRequest)+replyHeaderSize ||
		cap(buffer.data) > int(server.MaxRequest) || len(buffer.data) != 32 {
		t.Fatal("pooled frame exceeded its payload limit or exposed unused bytes")
	}
	copy(buffer.data, bytes.Repeat([]byte{0xa7}, 32))
	if !bytes.Equal(buffer.frame[16:], buffer.data) {
		t.Fatal("frame does not share the payload allocation")
	}
}

type countReplyConn struct {
	benchmarkConn
	writes int
}

func (c *countReplyConn) Write(data []byte) (int, error) {
	c.writes++
	return c.benchmarkConn.Write(data)
}
