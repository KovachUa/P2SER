# P2SER - Decentralized P2P Container Orchestrator

P2SER (Peer-to-Peer Service Edge Runtime) is a lightweight, decentralized (Masterless) container orchestrator built primarily for Edge and IoT environments (like Raspberry Pi, Orange Pi, and other SBCs). 

Unlike heavy orchestrators like Kubernetes, P2SER natively understands your standard `docker-compose.yaml`. You don't need to write thousands of lines of complex YAML manifests.

🌍 **[Українська версія (Ukrainian)](README.uk.md)**

## 🚀 Key Features

*   **P2P Architecture (Masterless):** No dedicated master nodes. All nodes are equal, and the cluster state is replicated using Raft consensus and Gossip protocol (`hashicorp/memberlist` and `hashicorp/raft`).
*   **Docker Compose Native:** Fully compatible with standard Docker Compose syntax. Deploy applications using the files you already have.
*   **Edge & IoT Optimized:** Written in Go and integrated directly with `containerd` (bypassing the Docker daemon) for maximum performance and minimal memory footprint on ARM64 and x86 devices.
*   **Warm Standby & High Availability:** Custom extensions (`x-k1n` / `x-p2ser`) allow configuring "warm standby" replicas that consume minimal resources until an active node fails.
*   **Zero-Touch Deployment:** Single-token cluster initialization (`p2ser init`). Nodes discover each other automatically via mDNS or UDP Broadcast in LAN.
*   **Built-in Web Dashboard:** Comes with a beautiful, cloud-like React UI for managing projects, deploying templates, and monitoring real-time metrics.
*   **Advanced Networking:** Built-in IPAM, CNI integration, and VXLAN overlay networks for seamless Pod-to-Pod communication across different physical nodes.
*   **Zero Trust Security:** Enforces mTLS encryption, encrypted state storage (`bbolt`), and container isolation (Rootless & eBPF). Includes a built-in Geo-IP filter.

## 📦 How to Describe a Service

P2SER keeps full compatibility with the Docker Compose standard. To use unique cluster features, we utilize official Extension fields (starting with `x-`).

### Example: Warm Standby
Run two active replicas and one "sleeping" (Warm Standby) replica that instantly takes over traffic if a server goes down.

```yaml
services:
  frontend:
    image: nginx:latest
    ports:
      - "80:80"
    deploy:
      replicas: 2
    x-p2ser:
      standby_replicas: 1
```

## 🛠 Installation & Bootstrap

1. **Initialize the Cluster (First Node):**
   ```bash
   p2ser init 'MY_SECRET_TOKEN'
   ```
2. **Join the Cluster (Other Nodes):**
   Simply run the same command on other machines in the same LAN. They will automatically discover the leader via Multicast/mDNS and join.
   ```bash
   p2ser init 'MY_SECRET_TOKEN'
   ```
3. **Deploy an App:**
   ```bash
   p2ser deploy -f ./docker-compose.yaml
   ```

## 🖥 Web Interface & API

P2SER includes a separated web dashboard (React + Vite) accessible over the API gateway.
*   **Visual Management:** Deploy apps from a template marketplace or custom Git repositories.
*   **Real-time Monitoring:** View live CPU/RAM metrics via Gossip protocol.
*   **Web Terminal:** One-click bash console directly in the browser.
*   **Project Isolation:** Group resources into virtual "Projects" (Namespaces) with resource quotas.

## 🛡 Architecture & Security

*   **Raft + Bbolt:** 100% replication of the transaction log to all nodes. 
*   **Distributed Locking:** Conflict avoidance using Compare-and-Swap (CAS) and Leases.
*   **Self-Healing:** Continuous level-triggered reconciliation. If a node dies, its workloads are instantly rescheduled.

---
*Designed for the Edge. Built for Simplicity.*
