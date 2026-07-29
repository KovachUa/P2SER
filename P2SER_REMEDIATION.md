# P2SER — Full Issue List for Remediation

**Repo:** https://github.com/KovachUa/P2SER
**Reviewed commit:** `b3455fe6ee52545148d988750047c7994c4fd900` (2026-07-20)
**Method:** static read-through of the entire backend (Go) and frontend (React/Vite), plus the build/install scripts and test suite. No live multi-node cluster was run, so a few timing-dependent effects (noted inline) are logically derived from the code, not empirically observed — re-test after fixing.

## How to use this document

The codebase already contains a prior audit's findings as inline comments, labeled `C-1`..`C-5` (Critical), `H-1`..`H-8` (High), `M-1`..`M-7` (Medium), `L-1`..`L-5` (Low). Most of those are already fixed correctly; a few are only partially fixed (noted below where relevant). This document **continues that same numbering** (`C-6` onward, etc.) so IDs stay unique and searchable across the codebase's own comment history.

Each item lists: **Location** (file:line as of the commit above — re-view the file before editing, since fixing earlier items may shift line numbers in the same file), **Current behavior**, **Impact**, and **Fix direction**. Fix directions describe the intended correct behavior; they are not full patches — write the actual code.

Several items share a root cause and should be fixed as a unit; these are marked **[linked]** and cross-referenced.

---

## CRITICAL

### C-6 — Raft bootstrap logic contradicts the documented cluster-join procedure
**Location:** `backend/cmd/p2ser/main.go:131` (`isBootstrap := command == "init"`), `backend/internal/cluster/raft.go:75` (`BootstrapCluster` call); `README.md` / `README.uk.md` (join-cluster instructions)
**Current:** Any node with no existing Raft state that is run with `p2ser init <token>` bootstraps its own independent single-node Raft cluster. The README tells users to run that exact same `init` command on every additional node to join the cluster.
**Impact:** Following the documented procedure literally produces N independent one-node "clusters" instead of one N-node cluster. Gossip (memberlist) still connects the nodes cosmetically, but Raft-backed state (deployed pods, config) does not replicate cluster-wide. The code path that *does* work correctly — a node running `start` (not `init`) is auto-discovered via mDNS and added as a voter by the existing leader through `NotifyJoin` → `AddVoter` in `network/delegate.go:85-104` — is never mentioned in the docs.
**Fix:** Minimal: fix the README to say `init` exactly once on the first node, `start` on every other node. More robust: before bootstrapping, have the node check via mDNS/gossip whether a cluster with this token is already reachable, and if so, join via the `start` path instead of bootstrapping regardless of which command was typed.

### C-7 — Node's own identity IP is hardcoded to loopback **[linked: C-8]**
**Location:** `backend/cmd/p2ser/main.go:251` — `network.NewNetworkManager(config.Name, "127.0.0.1")`, comment admits this is a placeholder
**Current:** Every node advertises `127.0.0.1` as its own address for networking/VXLAN purposes, regardless of its real LAN/physical IP.
**Impact:** Inter-node VXLAN routing cannot work — `network.go:102-113`'s `UpdateRouteTable` sets the route gateway to a peer's advertised IP, which is always loopback. Note: the prior audit's `L-2` fix in `containerd.go` removed a different hardcoded IP (`192.168.1.18`) and replaced it with "get localIP from NetworkManager" — but NetworkManager itself receives this same hardcoded `127.0.0.1`, so the underlying TODO was relocated, not resolved.
**Fix:** Discover the real outbound interface IP (e.g., dial a known-reachable address over UDP and read the local address of the socket, or read the interface associated with the default route) and pass that into `NewNetworkManager` instead of a literal.

### C-8 — VXLAN forwarding table (FDB) is never populated **[linked: C-7]**
**Location:** `backend/internal/network/network.go:69` (`SetupVXLAN`), `:102-113` (`UpdateRouteTable`)
**Current:** `UpdateRouteTable` only adds `ip route` entries; no `bridge fdb append` entries per remote VTEP and no multicast `Group` are ever configured on the vxlan0 device.
**Impact:** Even after C-7 is fixed, the kernel has no way to know which physical remote endpoint to encapsulate-and-send unknown-destination VXLAN traffic to. The overlay is very unlikely to forward real traffic between nodes without this.
**Fix:** For each known peer, add a static FDB entry (`bridge fdb append 00:00:00:00:00:00 dev vxlan0 dst <peerRealIP>`) when the peer is discovered/updated, or configure a multicast Group if the network supports it.

### C-9 — `raft.String()` misused as the local node's ID (2 call sites)
**Location:** `backend/internal/api/api.go:943` (`HandleExecPod`), `:385` (`HandleNodes`)
**Current:** `localNode := s.raftNode.String()`. Verified against `hashicorp/raft` source directly: `func (r *Raft) String() string { return fmt.Sprintf("Node at %s [%v]", r.localAddr, r.getState()) }` — a debug string, not the `ServerID`.
**Impact:** In `HandleExecPod`, `pod.NodeID != localNode` is always true, so the handler always treats the target pod as remote — even when it's on the same node — and proxies the exec/terminal WebSocket session to itself (dialing its own address with the token in the URL), which is likely to loop or fail. This breaks the web terminal for the common case of a single-node or freshly-deployed cluster. In `HandleNodes` the same bug exists in a redundant fallback branch for leader-labeling (lower impact — the primary check by address already works).
**Fix:** Add a `localNodeID string` field to the `APIServer` struct (around `api.go:51-57`), populated from the same `localID` used to build `config.LocalID = raft.ServerID(localID)` in `raft.go:27`, and pass it into `NewAPIServer`. Replace both `s.raftNode.String()` comparisons with this field.

### C-10 — Fencing (split-brain protection) defeats itself **[linked: C-11]**
**Location:** `backend/internal/engine/fencing.go` (`SetupFencing`), `agent.go:69` (`reconcile`), `agent.go:163` (`FenceStatefulPods`), `scheduler.go:333` (`renewLeases`)
**Current:** `FenceStatefulPods` stops a stateful container via containerd but never updates `pod.Status`/`LeaseExpiresAt` in the FSM. The same node's own `Agent.reconcile()` runs every 5s, checks only local containerd state via `IsContainerRunning`, sees "should be Running, isn't," and immediately restarts the just-fenced container. Separately, `Scheduler.renewLeases()` (also every 5s) renews the lease for any pod matching the node's ID + status, without checking whether the container is actually running — so the cluster-wide dead-node-detection mechanism (lease expiry) also never reflects the fenced state.
**Impact:** The documented purpose of this subsystem ("Пункт 5.4," split-brain protection) does not function in the scenario it exists for — a node fences and un-fences itself within ~5 seconds, entirely locally, with no real network partition required to undo it. It *does* still trigger (and then immediately reverse) on every ordinary Raft leader election, since `fencing.go` cannot distinguish a benign election from a genuine minority partition.
**Fix:** `FenceStatefulPods` must persist an explicit "fenced/intentionally-stopped" marker (e.g., a new pod status value, or a boolean field) via `UpdatePod`. `Agent.reconcile()` must check this marker and skip auto-restart while it's set. `renewLeases()` must check actual container state (like `reconcile()` does) before renewing — a fenced pod's lease should be allowed to expire so the rest of the cluster can correctly detect the node is (possibly) down.

### C-11 — Warm Standby promotion has no death-confirmation step **[linked: C-10]**
**Location:** `backend/internal/scheduler/scheduler.go:270` (`reconcileStandby`)
**Current:** A local Standby pod is promoted to Active purely because a same-App Active pod's `LeaseExpiresAt < now`. Lease expiry is not proof of death — it can be a transient network or scheduler hiccup while the old Active container keeps running fine.
**Impact:** This is a second, fully independent path to the exact split-brain condition fencing (C-10) is supposed to prevent — two "Active" instances of the same stateful app can end up running simultaneously, both potentially writing to shared storage.
**Fix:** Promotion needs a stronger signal than lease expiry alone — e.g., cross-check against gossip/memberlist's live-node table (is the old Active's node actually unreachable, not just slow to renew?), or require the old node to be confirmed fenced/gone before promoting, not just "hasn't renewed in 15s."

### C-12 — Generated systemd unit is broken (typo in section header)
**Location:** `backend/internal/system/systemd.go:33`
**Current:** `[compose.Service]` instead of `[Service]`.
**Impact:** systemd silently drops the entire section — `ExecStart=`, `Restart=`, resource limits, everything — because the section name is unrecognized. The generated unit will not start. This completely breaks the documented "survives power loss / auto-starts on boot" feature for edge/Raspberry Pi deployments.
**Fix:** Change to `[Service]`. While touching this file, also see L-10 (LimitCORE) and consider adding `User=`/`Group=` once H-11 is resolved.

### C-13 — No validation on container volume bind-mount sources
**Location:** `backend/internal/engine/containerd.go:27` (`resolveVolumeSrc`), `backend/internal/compose/compose_parser.go:136-146`
**Current:** An absolute path is used as-is. A relative path (`./` or `../`) is resolved via `filepath.Abs` with no confinement check. Only bare names are treated as "named volumes" confined to `/var/lib/p2ser/volumes`. `RunAsUser`/`UsernsRemap` (rootless mode) are opt-in per pod, defaulting to root with no user namespace.
**Impact:** Anyone holding the single, shared API token can deploy a compose file containing e.g. `volumes: ["/:/host"]` and mount the entire host filesystem read-write into a container, then use the (also token-gated) `/pod/exec` terminal to access it — full host compromise, available by design, not requiring any exploit.
**Fix:** This needs a policy decision (what host-path access, if any, should a deployer be allowed?). At minimum: reject absolute paths and `../`-escaping relative paths that fall outside an explicit, operator-configured allowlist; make non-root / user-namespace the default unless a deployer explicitly (and visibly) opts out.

### C-14 — Build pipeline is a second independent host-compromise path
**Location:** `backend/internal/builder/builder.go:62` (`docker compose build`), `:92` (`sudo ctr images import`)
**Current:** Any uploaded ZIP or cloned git repo has `docker compose -p <name> build` run directly on the host with no CPU/memory/time/network limits. Image import shells out to `sudo ctr -n p2ser images import -`, with no sudoers/NOPASSWD configuration provided anywhere in this repo.
**Impact:** Arbitrary `RUN` instructions in an attacker-supplied Dockerfile execute directly on the host — independent of C-13, this alone is enough for full compromise by anyone with the API token. Also reveals a full Docker Engine + compose plugin is required alongside containerd, which contradicts the "lightweight, containerd-only" positioning, and the bare `sudo` call will hang or fail without out-of-band sudoers setup this repo never performs.
**Fix:** Run builds in an isolated/rootless builder with explicit resource, time, and network limits (`--network=none` unless the project genuinely needs build-time network access). Replace the ad hoc `sudo ctr` call with whatever consistent privilege model is chosen for H-11.

---

## HIGH

### H-9 — Git-deploy SSRF: TOCTOU / DNS-rebinding bypass, plus fail-open
**Location:** `backend/internal/api/api.go:1287` (`validateGitURL`), `:254` (actual `git clone` call)
**Current:** `validateGitURL` resolves the hostname and checks the resolved IP(s) against a private-range list once, at validation time. The subsequent `git clone` independently re-resolves DNS when it actually connects. Additionally, if resolution fails during validation, the code explicitly lets the URL through ("might work at clone time; don't pre-block").
**Impact:** A domain with a short TTL can resolve to a public IP at validation time and a loopback/internal/link-local address by the time `git clone` connects — classic DNS-rebinding SSRF. The fail-open-on-resolution-failure path is a second, simpler bypass. (Scheme restriction to `https`/`git` is implemented correctly and does block the `ext::` transport RCE class — that part is fine, no change needed there.)
**Fix:** Resolve once, then force the actual connection to use that resolved IP (e.g., pin the IP and set the Host header / SNI appropriately, or use a custom dialer) so validation-time and connect-time addresses can't diverge. Reject rather than allow through when resolution fails.

### H-10 — Path traversal in `HandleGetLogs`
**Location:** `backend/internal/api/api.go:668-695`
**Current:** `logPath := "/tmp/p2ser_" + podID + ".log"`, built directly from the query parameter with no validation — unlike sibling handlers that already got this right (`HandleDirList`'s root-confinement, `HandleExecPod`'s `validPodID` regex).
**Impact:** `id=../../../var/log/auth` reads `/tmp/p2ser_../../../var/log/auth.log` → arbitrary `*.log`-suffixed files readable by the process (which appears to run as root elsewhere). Requires a valid API token (route is behind `AuthMiddleware`), so this is privilege escalation within the single shared token rather than fully anonymous access — still a real gap given the token has no scoping.
**Fix:** Apply the same `validPodID` regex already used in `HandleExecPod` to the `id` parameter here.

### H-11 — Rootless setup script is incompatible with the rest of the codebase
**Location:** `backend/setup_security.sh` (`setcap cap_net_admin+ep ./p2ser`); consumers: `containerd.go`/`network.go` (`exec.Command("iptables"/"ipset"/"sysctl", ...)`), `builder.go` (`sudo ctr`)
**Current:** `setcap` grants `CAP_NET_ADMIN` only to the `p2ser` binary. `iptables`/`ipset`/`sysctl`/`ctr` are invoked as separate child processes via `exec.Command`, which do not automatically inherit that capability (Linux capability inheritance across `exec` of unrelated binaries requires those binaries to carry their own file capabilities, which nothing here configures). containerd socket access typically also needs root or group membership, also not configured.
**Impact:** Following the documented "run as unprivileged `p2ser` user" setup most likely silently breaks port-forwarding (iptables DNAT), the Geo-IP sanctions filter (ipset), and image import (`ctr`) — and since those `exec.Command` calls' errors are already discarded (see M-10), the failures are invisible.
**Fix:** Pick one consistent model: (a) run as root and drop the rootless script, (b) grant file capabilities to the specific child binaries too / move iptables logic to an in-process netlink call (like `SetupVXLAN` already does), or (c) configure real, narrowly-scoped passwordless sudo and use it consistently everywhere these operations happen, not just in `builder.go`.

### H-12 — API token travels in the URL query string on every request, sourced from localStorage
**Location:** `ui/src/App.jsx:12` (`API_TOKEN` reads `localStorage.getItem('p2ser_api_token')`); every fetch in the file, e.g. lines ~59, 64, 183, 194, 201, 568, 622, 897, 1232 **[linked: H-13, M-14]**
**Current:** Every API call the real dashboard makes — stats, pods, logs, restart, delete, deploy-git, upload, compose, and the exec WebSocket — appends `?token=...`/`&token=...`.
**Impact:** Given the token is equivalent to host-root (C-13/C-14), it now also lands in browser history, any reverse-proxy access log, and any screenshot/screen-share of a network tab. `localStorage` is readable by any script on the page, which compounds directly with H-13.
**Fix:** Switch all plain HTTP calls to an `Authorization: Bearer` header — this is already correctly implemented, but unused, in `ui/src/api.js` (see M-14). For the WebSocket exec endpoint specifically (browsers can't set custom headers on WS upgrade), exchange a short-lived, single-use ticket over an authenticated HTTPS call instead of putting the long-lived static token in the URL.

### H-13 — Unescaped HTML injection via `dangerouslySetInnerHTML`
**Location:** `ui/src/App.jsx:759` (`highlightYaml`), `:1190` (consumer) **[linked: H-12]**
**Current:** `highlightYaml` wraps regex-matched fragments of raw text in `<span>` tags and the result is rendered via `dangerouslySetInnerHTML`, with no HTML-entity escaping anywhere in the pipeline.
**Impact:** Any YAML/env-var content containing `<`, `>`, etc. renders as live HTML/executes as JavaScript in the dashboard. Combined with H-12 (token in localStorage), a single successful XSS here is a full token theft — which, per C-13/C-14, means full cluster/host compromise.
**Fix:** HTML-escape the text first, then apply highlighting on top of the escaped string — or better, render syntax-highlighted tokens as React elements/children instead of raw HTML, avoiding `dangerouslySetInnerHTML` entirely.

### H-14 — `pod.Ready` is never persisted, breaking readiness-gated routing and dependency ordering
**Location:** `backend/internal/engine/agent.go:139-156` (`reconcile` computes `readinessOK` on a local copy, never calls `UpdatePod` for it — the TODO at `agent.go:155` undersells this; the value isn't even persisted locally, let alone sent anywhere), `backend/internal/scheduler/scheduler.go:247` (`checkDependencies` reads `p.Ready`), `backend/internal/dns/server.go:36` (never checks `Ready` at all, and never load-balances across replicas of the same App — always returns the first match)
**Current:** See above — three files each contribute to the same gap.
**Impact:** Any service configured with `depends_on: {condition: service_healthy}` (documented feature) never sees its dependency as satisfied and stays `Pending` forever. Readiness probes have zero effect on internal DNS routing, and multiple replicas of the same app are never load-balanced (DNS always returns the same instance).
**Fix:** `agent.go` must call `a.scheduler.UpdatePod(pod)` whenever `readinessOK != pod.Ready`, not only on status/IP changes. `dns/server.go` must filter by `pod.Ready` (and `Status == "Running"`) before selecting a candidate, and choose among all healthy matches (round-robin or random) rather than always the first.

---

## MEDIUM

### M-8 — README claims mTLS / Zero Trust; no TLS exists anywhere in the codebase
**Location:** `README.md` / `README.uk.md`; verified via `grep -rn "crypto/tls|x509" backend/` → zero matches
**Current:** Gossip is encrypted with a symmetric key derived from the bootstrap token (AES via memberlist) — not mTLS, no PKI, no mutual authentication. Raft transport and the HTTP API are plaintext TCP protected only by the bearer token. Only the optional WireGuard-based VPS ingress path is genuinely encrypted.
**Impact:** Documentation overstates actual transport security.
**Fix:** Either implement real TLS (an internal CA issuing per-node certs for Raft/API transport would actually deliver "Zero Trust"), or correct the docs to describe what's actually implemented.

### M-9 — Missing write-forwarding for two deploy handlers
**Location:** `api.go` — `HandleUploadSource`, `HandleDeployGit` (vs. `HandleApply`/`HandleCompose`, which correctly forward to leader)
**Current:** These two call `s.raftNode.Apply()` directly with no leader check/forward.
**Impact:** Hitting either endpoint on a non-leader node fails with a generic "Apply error" instead of transparently proxying, breaking the "masterless, any node works" UX for the two most common deploy paths.
**Fix:** Extract the leader-check-and-forward logic already in `HandleApply` into a shared helper; apply it here too.

### M-10 — Geo-IP sanctions filter fails open, silently
**Location:** `backend/internal/network/network.go:137` (`ApplyGeoIPSanctions`)
**Current:** Unconditionally flushes the ipset before attempting to re-download country blocklists via `sh -c "curl | awk | ipset restore"`. On download failure, the ipset stays empty (DROP rules become no-ops) and this is only logged. The prior `C-3` fix upgraded the URL to `https://` but the "Go http.Get + local cache" rewrite its own TODO calls for was never done.
**Impact:** Any transient network issue on the node (plausible for edge/IoT) silently turns the sanctions filter into a no-op with no operator-visible signal.
**Fix:** Don't flush until the new list is successfully fetched and parsed; cache the last-known-good list and fall back to it on fetch failure; surface fetch failures visibly (dashboard, not just a log line); replace the shell pipeline with native Go (`http.Get` + an ipset library, or check every command's exit status individually instead of just the last one in the pipe).

### M-11 — Weak token entropy + unchecked `rand.Read` error
**Location:** `main.go:378-380`
**Current:** `make([]byte, 8)` → 64 bits, hex-encoded; the `rand.Read` error is discarded.
**Impact:** 64 bits is low for a long-lived, non-rotating secret that (per C-13/C-14) is equivalent to host-root, especially since it also travels in URLs (H-12). An entropy-source failure would silently produce a weak/predictable token.
**Fix:** Use at least 32 bytes (256 bits); check and handle the `rand.Read` error by failing startup.

### M-12 — `BotProtectionMiddleware` is security theater
**Location:** `api.go` (`BotProtectionMiddleware`)
**Current:** Bans by substring match on `User-Agent` against a hardcoded list (nmap, sqlmap, gptbot, etc.).
**Impact:** Trivially bypassed by setting any normal User-Agent; stops only naive default-configured scanners and honest crawlers, not a deliberate attacker.
**Fix:** Fine to keep for crawler/noise filtering, but don't rely on it as an access control, and document that clearly so it doesn't create false confidence.

### M-13 — `HandleNodes` returns fake, random metrics
**Location:** `api.go:389`
**Current:** CPU/RAM values come from `rand.Intn` seeded by the node name, not from the real `NodeMetrics` already broadcast via gossip (received and logged, but never stored, in `network/delegate.go`'s `NotifyMsg`).
**Impact:** The "real-time monitoring" dashboard feature isn't connected to real data for the Nodes view.
**Fix:** Store the latest received `NodeMetrics` per peer (e.g., a mutex-guarded map updated from `NotifyMsg`) and read from that in `HandleNodes`.

### M-14 — Correct, header-based auth module (`api.js`) exists but is completely unused **[linked: H-12]**
**Location:** `ui/src/api.js` (entire file — confirmed via grep that `App.jsx` never imports it)
**Current:** A clean module using `Authorization: Bearer` headers and a configurable endpoint (`getEndpoint()` / localStorage key `p2ser_endpoint`) exists but is never wired into the actual `App` component, which uses a different localStorage key (`p2ser_api_token`) and query-param auth throughout.
**Impact:** Creates a false impression that H-12 was already fixed. It wasn't — the real UI path bypasses this module entirely.
**Fix:** Either migrate `App.jsx`'s fetch calls to use these helpers (completing the apparent original intent), or delete `api.js` if a different approach is planned — don't leave it implying an active fix that isn't wired in.

### M-15 — CNI plugin binaries downloaded with no checksum/signature verification
**Location:** `backend/internal/engine/cni.go:23` (`EnsureCNIPlugins`)
**Current:** `wget` + `tar` from a GitHub release URL, no hash comparison against the project's published checksums.
**Impact:** Supply-chain risk on first-run bootstrap — inconsistent with the security-consciousness shown elsewhere in this same codebase (WAF, Geo-IP, mTLS aspirations).
**Fix:** Fetch and verify the published sha256 checksum before extracting.

### M-16 — Resource quota sliders in the deploy UI are never sent to the backend
**Location:** `ui/src/App.jsx` (CPU/RAM/Storage/Replicas/Standby sliders, ~lines 500-543; actual submit bodies at ~573-577 and ~619-624)
**Current:** Slider values only populate a local-only, non-persisted "projects" card after submission; the real POST bodies to `/deploy-git` and `/upload` never include them.
**Impact:** Users reasonably expect these controls to affect the deployment; they do nothing. Actual replica/standby counts are controlled solely by the compose file's own `deploy.replicas` / `x-k1n-standby`.
**Fix:** Either remove these controls, or thread them through as overrides merged into the parsed compose before deployment.

### M-17 — Git branch field: state-key mismatch silently drops the intended default
**Location:** `ui/src/App.jsx:45` (`gitBranch: 'main'` in initial state) vs. `:462-463`, `:575` (all read/write `newProjectForm.branch`)
**Current:** The input and the submit payload consistently use `branch`, a key that is never initialized (`gitBranch` is initialized but otherwise unused).
**Impact:** The field opens blank instead of pre-filled with "main"; if left blank, `branch` is omitted from the JSON payload entirely (`JSON.stringify` drops `undefined` values), so the deployed branch silently depends on the server/git default. Typing an explicit value works correctly.
**Fix:** Rename the initial-state key to `branch: 'main'` (or make the input consistently use `gitBranch`) so the intended default actually applies.

### M-18 — Projects tab is pure ephemeral client-side state, not synced with the backend
**Location:** `ui/src/App.jsx` (`setProjects` at lines 262, 637 — no corresponding fetch anywhere)
**Current:** The list is populated only by the local deploy-modal submission and cleared only by local filtering.
**Impact:** Empty on page reload; never shows projects deployed via the CLI; the "Delete" button only removes the local card — it does not delete anything server-side, so a user can believe a project is torn down while its pods keep running.
**Fix:** Populate this tab from a real fetch (e.g., derived from `/pods`, grouped by App/project) and wire "Delete" to an actual removal API call.

### M-19 — Settings tab is partially decorative
**Location:** `ui/src/App.jsx:316` (hardcoded "Running" status), `:330` (hardcoded "4 Secrets active"), `:318` and `:332` (buttons with no `onClick`)
**Current:** Static placeholder text/status with no backing data or handlers; only the API Token field actually functions.
**Impact:** Misleading — the ingress controller could be down and the UI would still claim "Running".
**Fix:** Wire to real state, or mark clearly as "coming soon" until implemented.

---

## LOW

### L-6 — Every node always runs a full WAN gossip ring
**Location:** `main.go:291-314`
**Current:** A `memberlist.DefaultWANConfig()`-based ring starts unconditionally, even when federation (`-join`) is never used.
**Impact:** Unnecessary resource overhead on Raspberry Pi-class hardware.
**Fix:** Only start the WAN ring when federation is actually configured.

### L-7 — Unchecked `Apply()` error in rolling-update old-pod marking
**Location:** `api.go:517`
**Impact:** On failure, both old and new pod could remain active simultaneously.
**Fix:** Check and handle the error, consistent with a neighboring `Apply()` call in the same function.

### L-8 — HTTP healthcheck leaks response bodies
**Location:** `engine/agent.go:49` (`checkHealth`, http branch)
**Impact:** Connection/fd leak, accumulating since this runs every 5s per probed pod.
**Fix:** `defer resp.Body.Close()`.

### L-9 — CLI sends the client's local filesystem path as a server-side working directory
**Location:** `backend/internal/deploy/deploy.go:34`
**Impact:** Relative volume paths in a compose file only resolve correctly on the server if client and target node share the same filesystem layout — effectively only correct for localhost deployments; for a real remote node this silently creates a nonsensical directory via `os.MkdirAll`.
**Fix:** Document the limitation, or package relative-path volume contents into the uploaded artifact instead.

### L-10 — systemd unit sets `LimitNPROC=infinity` / `LimitCORE=infinity`
**Location:** `system/systemd.go:40-41`
**Impact:** An uncapped core dump could write the API token / WireGuard private key (both live in process memory) unencrypted to disk on crash.
**Fix:** Set `LimitCORE=0` unless core dumps are intentionally needed.

### L-11 — Operationally important values are hardcoded instead of configurable
**Location:** pod subnet `10.88.0.0/16` and bridge name `p2ser0` (`cni.go`), DNS forwarder `8.8.8.8` with no DoT/local-resolver fallback (`dns/server.go:62`), sanctioned country list (`network.go`)
**Impact:** Two independent P2SER clusters on the same LAN collide by default; unmatched DNS queries leave the node in plaintext to Google's resolver unconditionally.
**Fix:** Move into `config.yaml` with sensible defaults.

### L-12 — `ui/fix.py` is a fragile, dangerous-if-rerun script left in the repo
**Location:** `ui/fix.py`
**Current:** One-off script that relocates the `TerminalWindow` function using brittle line-position heuristics, and unconditionally appends closing-tag lines at the end regardless of whether extraction matched anything.
**Impact:** Re-running it against the already-fixed file would corrupt it with stray duplicate closing tags.
**Fix:** Delete — it already served its one-time purpose.

### L-13 — Floating base image tag in the builder compose file
**Location:** `backend/docker-compose.builder.yaml` (`image: golang:alpine`)
**Impact:** Non-reproducible builds over time.
**Fix:** Pin to a specific version/digest, e.g. `golang:1.22-alpine`.

### L-14 — Dead/unreachable "Marketplace" tab
**Location:** `ui/src/App.jsx` (`IconMarketplace` at `:23`, referenced only in the header title switch at `:126`; no nav button, no content block)
**Fix:** Remove, or finish wiring if still planned.

### L-15 — `API_BASE` hardcoded to port 8002
**Location:** `ui/src/App.jsx:13-14`
**Impact:** Not configurable from the UI (unlike the token); breaks if the API isn't reachable at `<same-hostname>:8002` (e.g., behind a reverse proxy on another port).
**Fix:** Make configurable — the groundwork already exists, unused, in `api.js`'s `getEndpoint()`.

### L-16 — Test suite doesn't cover correctness or security properties
**Location:** `ui/src/__tests__/*.test.jsx` (all four files)
**Current:** Tests check that UI elements render/respond to clicks, that localStorage round-trips, and that pure converter functions transform data correctly. None assert on actual submitted fetch/WebSocket payloads (would have caught M-16/M-17). `security.test.jsx` specifically tests that the insecure token-in-URL/localStorage design "works as intended," not any real security property (no XSS/CSRF/auth-bypass coverage).
**Impact:** Green tests currently create a false sense of coverage.
**Fix:** Add assertions on actual outgoing request payloads for form submissions; add real security tests (HTML-escaping before render, headers vs. URL params for auth) once the above items are fixed.

---

## Suggested fix order

1. **C-12** (one-line systemd typo) — trivial, unblocks reboot resilience immediately.
2. **C-9** (add `localNodeID` to `APIServer`) — small, self-contained, fixes the web terminal.
3. **C-6** (docs) — trivial doc fix; decide separately if the more robust code-level fix is worth doing.
4. **C-13 / C-14** together — same underlying trust-model question (what should a deployer with the API token be allowed to touch on the host); needs a policy decision before code changes.
5. **C-10 / C-11** together — HA/fencing subsystem; touches 3 files, needs to be designed as one coherent change, not three independent patches.
6. **C-7 / C-8** together — real node IP discovery + FDB population; the networking story doesn't work until both land.
7. **H-12 / H-13 / M-14** together (frontend security) — wire `App.jsx` to header-based auth (finish or replace `api.js`), fix `highlightYaml` escaping.
8. Everything else can be done independently and in any order; H-14, M-9, M-10, M-13 are all "feature X looks implemented but isn't actually connected end-to-end" — similar shape, worth doing as a batch.
