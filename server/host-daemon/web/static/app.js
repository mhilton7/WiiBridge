(() => {
  if ("scrollRestoration" in history) history.scrollRestoration = "manual";
  const scrollKey = `wiibridge:scroll:${window.location.pathname}`;
  const rememberScroll = () => {
    try {
      window.sessionStorage.setItem(scrollKey, String(window.scrollY));
    } catch (_) {}
  };
  const restoreScroll = () => {
    let saved = null;
    try {
      saved = window.sessionStorage.getItem(scrollKey);
      window.sessionStorage.removeItem(scrollKey);
    } catch (_) {}
    if (saved === null) return;
    const top = Number(saved);
    if (!Number.isFinite(top)) return;
    window.requestAnimationFrame(() => {
      window.requestAnimationFrame(() => window.scrollTo(0, top));
    });
  };
  window.addEventListener("beforeunload", rememberScroll);
  document.addEventListener("submit", rememberScroll, true);
  document.addEventListener("click", (event) => {
    const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
    if (!link) return;
    try {
      if (new URL(link.href, window.location.href).pathname === window.location.pathname) {
        rememberScroll();
      }
    } catch (_) {}
  }, true);

  document.querySelectorAll("details[data-persist-details]").forEach((details) => {
    const stateKey = `wiibridge:details:${details.dataset.persistDetails}`;
    try {
      const saved = window.sessionStorage.getItem(stateKey);
      if (saved !== null) details.open = saved === "open";
    } catch (_) {}
    const summary = details.querySelector(":scope > summary");
    let summaryTop = null;
    if (summary) {
      summary.addEventListener("click", () => {
        summaryTop = summary.getBoundingClientRect().top;
      });
    }
    details.addEventListener("toggle", () => {
      try {
        window.sessionStorage.setItem(stateKey, details.open ? "open" : "closed");
      } catch (_) {}
      if (summaryTop === null || !summary) return;
      const previousTop = summaryTop;
      summaryTop = null;
      window.requestAnimationFrame(() => {
        window.scrollBy(0, summary.getBoundingClientRect().top - previousTop);
      });
    });
  });
  restoreScroll();

  const panel = document.getElementById("pi-live");
  const text = (id, value) => {
    const element = document.getElementById(id);
    if (element) element.textContent = value;
  };
  const yes = (value, enabled, disabled) => value ? enabled : disabled;
  const automaticSwitch = panel && panel.dataset.automaticSwitch === "true";
  const switchButtons = document.querySelectorAll("[data-profile-switch]");
  const setSwitchReady = (ready) => {
    if (!automaticSwitch) return;
    switchButtons.forEach((button) => {
      button.disabled = button.dataset.baseDisabled === "true" || !ready;
    });
    const warning = document.getElementById("pi-switch-warning");
    if (warning) warning.hidden = ready;
  };
  async function refreshPi() {
    if (!panel) return;
    const dot = document.getElementById("pi-dot");
    try {
      const response = await fetch(panel.dataset.statusUrl, {
        headers: {"Accept": "application/json"}, cache: "no-store"
      });
      if (!response.ok) throw new Error("unavailable");
      const value = await response.json();
      const pi = value.pi;
      dot.classList.remove("offline");
      text("pi-connection", "Connected");
      text("pi-state", pi.state || "unknown");
      text("pi-export", pi.export_mode || "not selected");
      text("pi-storage", yes(pi.nbd_connected, "NBD connected", "NBD disconnected") +
        " · " + yes(pi.usb_attached, "USB attached", "USB detached"));
      text("pi-usb", (pi.usb_controller || "none") + " · " + (pi.usb_state || "unknown"));
      text("pi-board", pi.detected_board || "unknown");
      text("pi-addresses", (pi.addresses || []).join(", ") || "none");
      text("pi-provision", yes(pi.provisioned, "Ready", "Incomplete"));
      text("pi-attach", yes(pi.auto_attach, "Enabled", "Disabled"));
      text("pi-updated", "Updated " + new Date().toLocaleTimeString());
      setSwitchReady(pi.state === "ready" && pi.board_compatible &&
        pi.provisioned && pi.wifi_provisioned &&
        pi.usb_controller && pi.usb_controller !== "none" &&
        pi.usb_state && pi.usb_state !== "unknown" &&
        pi.usb_state !== "unavailable");
    } catch (_) {
      dot.classList.add("offline");
      text("pi-connection", "Unavailable");
      text("pi-updated", "Retrying");
      setSwitchReady(false);
    }
  }
  const build = document.querySelector("[data-build-url]");
  async function refreshBuild() {
    if (!build) return;
    try {
      const response = await fetch(build.dataset.buildUrl, {
        headers: {"Accept": "application/json"}, cache: "no-store"
      });
      if (!response.ok) return;
      const value = await response.json();
      text("gc-build-state", value.progress.state);
      text("gc-current", value.progress.current_title || "Finalizing");
      text("gc-count", `${value.progress.titles_processed} / ${value.progress.total_titles} titles · ${value.progress.files_mapped} files · ${value.progress.extent_count} extents · ${value.progress.metadata_bytes_generated} metadata bytes · ${value.progress.current_phase}`);
      const progress = document.getElementById("gc-progress");
      if (progress) {
        progress.max = value.progress.total_titles || 1;
        progress.value = value.progress.titles_processed;
      }
      if (value.progress.state !== "Building") {
        rememberScroll();
        window.location.reload();
      }
    } catch (_) {}
  }

  const formatBytes = (value) => {
    const bytes = Number(value || 0);
    if (!Number.isFinite(bytes) || bytes < 1024) return `${Math.max(bytes, 0).toFixed(0)} B`;
    const units = ["KiB", "MiB", "GiB"];
    let amount = bytes;
    let unit = -1;
    do {
      amount /= 1024;
      unit += 1;
    } while (amount >= 1024 && unit < units.length - 1);
    return `${amount.toFixed(1)} ${units[unit]}`;
  };
  const formatTime = (value) => value ? new Date(value).toLocaleString() : "Never";
  const revisionSummary = (value) => {
    const revision = String(value || "unknown");
    return revision.length > 12 ? revision.slice(0, 12) : revision;
  };
  const versionRevision = (id, productVersion, revision) => {
    const element = document.getElementById(id);
    if (!element) return;
    const version = productVersion || "unknown";
    const fullRevision = revision || "unknown";
    element.textContent = `${version} · ${revisionSummary(fullRevision)}`;
    element.title = `${version} · ${fullRevision}`;
  };

  const sourcePanels = document.querySelectorAll(".library-source");
  async function refreshSource() {
    if (!sourcePanels.length) return;
    try {
      const response = await fetch("/api/v1/sources", {
        headers: {"Accept": "application/json"}, cache: "no-store"
      });
      if (!response.ok) return;
      const value = await response.json();
      sourcePanels.forEach((panel) => {
        const platform = panel.dataset.platform;
        const source = (value.sources || {})[platform] || value.source || {};
        const prefix = `source-${platform}-`;
        text(prefix + "state", source.state || "unknown");
        text(prefix + "path", source.configured_root_path || "unknown");
        text(prefix + "last-success", formatTime(source.last_successful_scan));
        text(prefix + "last-attempt", formatTime(source.last_attempted_scan));
        text(prefix + "game-count", String(source.last_successful_item_count || 0));
        text(prefix + "affected", String(value[`affected_${platform}_games`] || 0));
        text(prefix + "error", source.failure_code ?
          `${source.failure_code} · ${source.failure_message || ""}` : "None");
      });
    } catch (_) {}
  }

  const compatibilityPanel = document.getElementById("compatibility-panel");
  async function refreshCompatibility() {
    if (!compatibilityPanel) return;
    try {
      const response = await fetch(compatibilityPanel.dataset.statusUrl, {
        headers: {"Accept": "application/json"}, cache: "no-store"
      });
      if (!response.ok) return;
      const value = await response.json();
      const host = value.host || {};
      const firmware = value.firmware || {};
      text("compat-state", value.status || "unknown");
      versionRevision("compat-host", host.productVersion, host.revision);
      text("compat-host-protocol", `${host.protocolMin || "?"}–${host.protocolMax || "?"}`);
      versionRevision("compat-firmware", firmware.productVersion, firmware.revision);
      text("compat-board", firmware.board || "unknown");
      text("compat-firmware-protocol", firmware.protocolMin ?
        `${firmware.protocolMin}–${firmware.protocolMax}` : "unknown");
      text("compat-negotiated", value.negotiatedProtocol == null ?
        "None" : String(value.negotiatedProtocol));
      text("compat-checked", formatTime(value.checkedAt));
      const reasons = [...(value.errors || []), ...(value.warnings || [])]
        .map((item) => `${item.code}: ${item.message}`);
      text("compat-reasons", reasons.join(" · ") || "None recorded");
      text("compat-host-capabilities", (host.capabilities || []).join(" · ") || "None");
      text("compat-firmware-capabilities", (firmware.capabilities || []).join(" · ") || "None");
    } catch (_) {}
  }

  const savePanel = document.getElementById("save-overlay");
  let saveBackups = {};
  const updateBackupOptions = () => {
    const object = document.querySelector("[data-save-object]")?.value || "";
    document.querySelectorAll("[data-save-backup]").forEach((select) => {
      select.replaceChildren();
      (saveBackups[object] || []).forEach((backup) => {
        const option = document.createElement("option");
        option.value = backup.name;
        option.textContent = `${formatTime(backup.createdAt)} · ${backup.reason} · ${backup.sha256.slice(0, 12)}`;
        select.append(option);
      });
    });
    const link = document.getElementById("save-download-link");
    if (link) link.href = `/api/v1/gamecube/saves/download?object_id=${encodeURIComponent(object)}`;
  };
  async function refreshSaves() {
    if (!savePanel) return;
    try {
      const response = await fetch(savePanel.dataset.statusUrl, {
        headers: {"Accept": "application/json"}, cache: "no-store"
      });
      const value = await response.json();
      text("save-mode", value.selection?.mode || "unknown");
      const blocked = document.getElementById("save-blocked");
      if (blocked) {
        blocked.hidden = !value.blocked_reason;
        blocked.textContent = value.blocked_reason || "";
      }
      const statuses = value.statuses || [];
      saveBackups = value.backups || {};
      const body = document.getElementById("save-status-body");
      if (body) {
        body.replaceChildren();
        if (!statuses.length) {
          const row = body.insertRow();
          const cell = row.insertCell();
          cell.colSpan = 11;
          cell.textContent = value.selection?.mode === "physical" ?
            "Physical mode keeps the GameCube export fully read-only." : "No managed cards are available.";
        }
        statuses.forEach((status) => {
          const row = body.insertRow();
          [
            status.id,
            status.game_id || status.shared_card_name || "shared",
            formatBytes(status.card_size),
            status.integrity_state,
            status.dirty ? `${status.dirty_blocks} blocks` : "clean",
            status.recovery_state,
            formatTime(status.last_successful_flush),
            formatTime(status.last_successful_backup),
            String(status.backup_count || 0),
            (status.current_checksum || "").slice(0, 16),
            status.error_code ? `${status.error_code}: ${status.current_error || ""}` : "None"
          ].forEach((value) => {
            const cell = row.insertCell();
            cell.textContent = value;
          });
        });
      }
      const previous = document.querySelector("[data-save-object]")?.value || "";
      document.querySelectorAll("[data-save-object]").forEach((select) => {
        select.replaceChildren();
        statuses.forEach((status) => {
          const option = document.createElement("option");
          option.value = status.id;
          option.textContent = status.id;
          option.selected = status.id === previous;
          select.append(option);
        });
      });
      updateBackupOptions();
    } catch (_) {}
  }
  document.querySelectorAll("[data-save-object]").forEach((select) => {
    select.addEventListener("change", updateBackupOptions);
  });
  const uploadForm = document.getElementById("save-upload-form");
  if (uploadForm && savePanel) {
    uploadForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const object = uploadForm.querySelector("[data-save-object]")?.value || "";
      if (!object) return;
      const form = new FormData(uploadForm);
      form.delete("object_id");
      const response = await fetch(
        `/api/v1/gamecube/saves/upload?object_id=${encodeURIComponent(object)}`, {
          method: "POST", body: form,
          headers: {"X-WiiBridge-CSRF": savePanel.dataset.csrf, "Accept": "application/json"}
        });
      if (!response.ok) {
        window.alert(await response.text());
        return;
      }
      await refreshSaves();
    });
  }

  const performancePanel = document.getElementById("performance-panel");
  async function refreshPerformance() {
    if (!performancePanel) return;
    try {
      const response = await fetch(performancePanel.dataset.summaryUrl, {
        headers: {"Accept": "application/json"}, cache: "no-store"
      });
      if (!response.ok) return;
      const value = await response.json();
      const host = value.host || {};
      const nbd = host.nbd || {};
      const runtime = host.runtime || {};
      const pi = value.pi || null;
      const session = value.session || {};
      text("perf-platform", session.platform || "No active export session");
      text("perf-duration", value.session_active && session.start_time ?
        `${Math.max(0, Math.floor((Date.now() - new Date(session.start_time).getTime()) / 1000))} s` :
        "No active session");
      text("perf-throughput", `${formatBytes(nbd.rates?.bytes_per_second_1m || 0)}/s (rolling 1m)`);
      text("perf-latency", `${(nbd.latency?.p95_us || 0).toFixed(0)} / ${(nbd.latency?.p99_us || 0).toFixed(0)} µs`);
      text("perf-source", value.source?.state || "unknown");
      text("perf-nbd", String(nbd.counters?.active_connections || 0) + " active");
      text("perf-pi", value.pi_state || "unavailable");
      text("perf-usb", pi?.usb_gadget_state || "unavailable");
      text("perf-host-memory", `${formatBytes(runtime.go_heap_bytes)} / ${formatBytes(runtime.container_memory_limit_bytes)}`);
      text("perf-pi-memory", pi ?
        `${formatBytes(pi.memory_used_bytes)} / ${formatBytes(pi.memory_total_bytes)} · ${pi.temperature_celsius || 0} °C` :
        "Unavailable");
      const path = document.getElementById("perf-data-path");
      if (path) {
        path.replaceChildren();
        (value.data_path || []).forEach((stage) => {
          const item = document.createElement("span");
          const throughput = stage.throughput_bytes_per_second == null ?
            "throughput unavailable" :
            `${formatBytes(stage.throughput_bytes_per_second)}/s`;
          const latency = stage.latency_p99_us == null ?
            "latency unavailable" : `P99 ${Number(stage.latency_p99_us).toFixed(0)} µs`;
          item.textContent = `${stage.stage}: ${stage.state} · ${throughput} · ${latency}` +
            ` · ${stage.error_count == null ? "errors unavailable" : `${stage.error_count} errors`}` +
            ` · ${stage.measurement || "unavailable"} · ${formatTime(stage.last_update)}`;
          path.append(item);
        });
      }
      const warnings = document.getElementById("perf-warnings");
      if (warnings) {
        warnings.replaceChildren();
        const items = value.warnings || [];
        if (!items.length) items.push({code: "OK", message: "No deterministic warning threshold is active."});
        items.forEach((warning) => {
          const item = document.createElement("li");
          item.textContent = `${warning.code}: ${warning.message}`;
          warnings.append(item);
        });
      }
      text("perf-host-details", JSON.stringify(host, null, 2));
      text("perf-pi-details", pi ? JSON.stringify(pi, null, 2) :
        "PERF-PI-METRICS-UNAVAILABLE");
    } catch (_) {}
    try {
      const response = await fetch("/api/performance/sessions?limit=20", {
        headers: {"Accept": "application/json"}, cache: "no-store"
      });
      if (!response.ok) return;
      const value = await response.json();
      const body = document.getElementById("perf-session-body");
      if (!body) return;
      body.replaceChildren();
      const sessions = value.sessions || [];
      if (!sessions.length) {
        const row = body.insertRow();
        const cell = row.insertCell();
        cell.colSpan = 8;
        cell.textContent = "No retained sessions";
      }
      sessions.forEach((session) => {
        const row = body.insertRow();
        [
          session.session_id, formatTime(session.start_time), session.platform,
          formatBytes(session.total_bytes), String(session.read_count || 0),
          `${(session.p95_latency_us || 0).toFixed(0)} µs`,
          `${(session.p99_latency_us || 0).toFixed(0)} µs`,
          session.final_outcome
        ].forEach((value) => {
          const cell = row.insertCell();
          cell.textContent = value;
        });
      });
    } catch (_) {}
  }

  const pollVisible = (refresh, interval) => {
    let pending = false;
    const poll = async () => {
      if (document.hidden || pending) return;
      pending = true;
      try {
        await refresh();
      } finally {
        pending = false;
      }
    };
    poll();
    window.setInterval(poll, interval);
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden) poll();
    });
  };
  pollVisible(refreshPi, 10000);
  pollVisible(refreshBuild, 3000);
  pollVisible(refreshSource, 15000);
  pollVisible(refreshCompatibility, 15000);
  pollVisible(refreshSaves, 10000);
  pollVisible(refreshPerformance,
    Number(performancePanel?.dataset.refreshMs || 5000));
})();
