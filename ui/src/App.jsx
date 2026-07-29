import { useState, useEffect, useRef } from 'react'
import * as yaml from 'js-yaml'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import JSZip from 'jszip'
import '@xterm/xterm/css/xterm.css'
import './index.css'

// ── Centralized API configuration ──────────────────────────────────────────
// SECURITY: Token is read from localStorage (set via Settings page).
// Never commit real tokens to source code.
const API_TOKEN = () => localStorage.getItem('p2ser_api_token') || '';
const getApiBase = () => {
  const ep = localStorage.getItem('p2ser_endpoint');
  return ep ? ep.replace(/\/$/, '') : `${window.location.protocol}//${window.location.hostname}:8002`;
};
const API_BASE = getApiBase();
const API_WS_BASE = API_BASE.replace(/^http/, 'ws');

const authFetch = (url, options = {}) => {
  return fetch(url, {
    ...options,
    headers: {
      ...options.headers,
      'Authorization': `Bearer ${API_TOKEN()}`
    }
  });
};

// Icons (using simple SVG for zero-dependency but keeping aesthetics)
const IconDashboard = () => (
  <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="3" width="7" height="9"></rect><rect x="14" y="3" width="7" height="5"></rect><rect x="14" y="12" width="7" height="9"></rect><rect x="3" y="16" width="7" height="5"></rect></svg>
)
const IconProjects = () => (
  <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"></path></svg>
)

const IconSettings = () => (
  <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="3"></circle><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"></path></svg>
)

const IconConvert = () => (
  <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><polyline points="17 1 21 5 17 9"></polyline><path d="M3 11V9a4 4 0 0 1 4-4h14"></path><polyline points="7 23 3 19 7 15"></polyline><path d="M21 13v2a4 4 0 0 1-4 4H3"></path></svg>
)

const IconServer = () => (
  <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect x="2" y="2" width="20" height="8" rx="2" ry="2"></rect><rect x="2" y="14" width="20" height="8" rx="2" ry="2"></rect><line x1="6" y1="6" x2="6.01" y2="6"></line><line x1="6" y1="18" x2="6.01" y2="18"></line></svg>
)

export default function App() {
  const [activeTab, setActiveTab] = useState('dashboard')
  const [showProjectModal, setShowProjectModal] = useState(false)
  const [newProjectForm, setNewProjectForm] = useState({
    name: '',
    sourceType: 'folder',
    gitUrl: '',
    branch: 'main',
    envVars: '',
    mode: 'folder'
  })
  const [selectedFiles, setSelectedFiles] = useState([])
  const [logsModal, setLogsModal] = useState({show: false, title: '', content: ''})
  const [terminalModal, setTerminalModal] = useState({show: false, podId: '', podName: ''})
  const [realStats, setRealStats] = useState({cpu: '0.00', ram_used: '0 GB', ram_total: '0 GB', active_pods: 0})
  const [realPods, setRealPods] = useState([])


  useEffect(() => {
    const fetchData = async () => {
      try {
        const statsRes = await authFetch(`${API_BASE}/stats`);
        if (statsRes.ok) {
          const s = await statsRes.json();
          setRealStats(prev => ({...prev, cpu: s.cpu, ram_used: s.ram_used, ram_total: s.ram_total, geo_ip_error: s.geo_ip_error}));
        }
        const podsRes = await authFetch(`${API_BASE}/pods`);
        if (podsRes.ok) {
          const p = await podsRes.json();
          let podsList = [];
          if (Array.isArray(p)) {
              podsList = p.map(pod => ({ id: pod.id, name: pod.app || pod.id, image: pod.image, status: pod.status, uptime: 'Live', project: pod.project, app: pod.app, cpu_req: pod.cpu_req, ram_req: pod.ram_req, dir: pod.dir }));
          } else if (p.namespaces) {
            Object.keys(p.namespaces).forEach(ns => {
              p.namespaces[ns].forEach(pod => {
                podsList.push({ id: pod.id, name: pod.name || pod.app || pod.id, image: pod.image, status: pod.status, uptime: 'Live', project: pod.project || ns, app: pod.app, cpu_req: pod.cpu_req, ram_req: pod.ram_req, dir: pod.dir });
              });
            });
          }
          setRealPods(podsList);
          setRealStats(prev => ({...prev, active_pods: podsList.length}));

          // M-18: Build projects from pods dynamically
          const projMap = {};
          podsList.forEach(pod => {
            const pName = pod.project || pod.app || 'default';
            if (!projMap[pName]) {
              projMap[pName] = {
                id: pName,
                name: pName,
                dir: pod.dir || '',
                cpuQuota: 0,
                ramQuota: 0,
                podsCount: 0
              };
            }
            projMap[pName].podsCount++;
            projMap[pName].cpuQuota += (pod.cpu_req || 0);
            projMap[pName].ramQuota += (pod.ram_req || 0);
          });
          const fetchedProjects = Object.values(projMap).map(p => ({
            ...p,
            cpuQuota: p.cpuQuota > 0 ? p.cpuQuota + ' Cores' : 'Auto',
            ramQuota: p.ramQuota > 0 ? p.ramQuota + ' MB' : 'Auto'
          }));
          setProjects(fetchedProjects);
        }
      } catch (err) {
        console.error("Backend offline or not reachable");
      }
    };
    fetchData();
    const interval = setInterval(fetchData, 5000);
    return () => clearInterval(interval);
  }, []);

  // Mock data for UI demonstration
  const stats = [
    { title: 'Host CPU Load', value: realStats.cpu, trend: 'Current', status: 'Normal' },
    { title: 'Host RAM Used', value: realStats.ram_used, trend: 'of ' + realStats.ram_total, status: 'Normal' },
    { title: 'Active Pods', value: realStats.active_pods.toString(), trend: 'Containers', status: 'Running' }
  ]

  const [projects, setProjects] = useState([])

  return (
    <div className="app-container">
      {/* Sidebar */}
      <aside className="sidebar">
        <div className="logo">P2SER</div>
        <nav className="nav-menu">
          <li className={`nav-item ${activeTab === 'dashboard' ? 'active' : ''}`} onClick={() => setActiveTab('dashboard')}>
            <IconDashboard /> Dashboard
          </li>
          <li className={`nav-item ${activeTab === 'projects' ? 'active' : ''}`} onClick={() => setActiveTab('projects')}>
            <IconProjects /> Projects
          </li>

          <li className={`nav-item ${activeTab === 'converter' ? 'active' : ''}`} onClick={() => setActiveTab('converter')}>
            <IconConvert /> Compose→P2SER
          </li>
          <li className={`nav-item ${activeTab === 'settings' ? 'active' : ''}`} onClick={() => setActiveTab('settings')}>
            <IconSettings /> Settings
          </li>
        </nav>
      </aside>

      {/* Main Content */}
      <main className="main-content">
        <header className="topbar animate-fade-in delay-1">
          <h1 className="page-title">
            {activeTab === 'dashboard' && 'Cluster Overview'}
            {activeTab === 'projects' && 'Namespaces & Projects'}
            {activeTab === 'converter' && 'Compose → P2SER Converter'}
            {activeTab === 'settings' && 'Cluster Settings'}
          </h1>
          <div className="user-profile">
            <span>Admin</span>
            <div className="avatar">A</div>
          </div>
        </header>

        {activeTab === 'dashboard' && (
          <>
            {realStats.geo_ip_error && (
              <div style={{
                background: 'rgba(245, 158, 11, 0.1)',
                border: '1px solid var(--warning)',
                color: 'var(--warning)',
                padding: '16px',
                borderRadius: '12px',
                marginBottom: '24px',
                display: 'flex',
                alignItems: 'center',
                gap: '12px'
              }}>
                <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>
                <div>
                  <strong>Geo-IP Sanctions Filter Warning:</strong> {realStats.geo_ip_error}
                </div>
              </div>
            )}
            <div className="dashboard-grid animate-fade-in delay-2">
              {stats.map((stat, i) => (
                <div className="glass-card" key={i}>
                  <div className="card-header">
                    {stat.title}
                    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"></polyline></svg>
                  </div>
                  <div className="card-value">{stat.value}</div>
                  <div className="card-subtitle">
                    <span className="trend-up">↑ {stat.trend}</span>
                    <span>since last hour</span>
                  </div>
                </div>
              ))}
            </div>

            <div className="pods-section animate-fade-in delay-3">
              <h2 className="section-title">Running Pods</h2>
              {realPods.map((pod) => (
                <div className="pod-item" key={pod.id} style={{flexDirection: 'column', alignItems: 'stretch'}}>
                  <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
                    <div className="pod-info">
                      <div className="pod-icon">
                        <IconServer />
                      </div>
                      <div>
                        <div className="pod-name">{pod.name}</div>
                        <div className="pod-image">{pod.image} • {pod.id}</div>
                      </div>
                    </div>
                    <div style={{display: 'flex', alignItems: 'center', gap: '24px'}}>
                      <div style={{color: 'var(--text-secondary)', fontSize: '13px'}}>
                        Uptime: {pod.uptime}
                      </div>
                      <div className={`status-badge status-${pod.status}`}>
                        {pod.status === 'running' ? 'Running' : 'Pending'}
                      </div>
                    </div>
                  </div>
                  
                  <div style={{display: 'flex', gap: '8px', borderTop: '1px solid var(--border-color)', paddingTop: '16px', marginTop: '16px', width: '100%', justifyContent: 'flex-end'}}>
                    <button style={{background: 'rgba(59, 130, 246, 0.1)', border: '1px solid var(--accent-primary)', color: 'var(--accent-primary)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '13px'}} onClick={() => setTerminalModal({show: true, podId: pod.id, podName: pod.name})}>Terminal</button>
                    <button style={{background: 'rgba(255, 255, 255, 0.05)', border: '1px solid var(--border-color)', color: 'var(--text-primary)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '13px'}} onClick={async () => {
                        try {
                            const res = await authFetch(`${API_BASE}/pod/logs?id=${pod.id}`);
                            if (res.ok) {
                                const text = await res.text();
                                setLogsModal({show: true, title: 'Logs for ' + pod.name, content: text || '(Empty)'});
                            } else {
                                alert("Failed to fetch logs");
                            }
                        } catch (e) { alert("Error: " + e.message); }
                    }}>Logs</button>
                    <button style={{background: 'rgba(245, 158, 11, 0.1)', border: '1px solid var(--warning)', color: 'var(--warning)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '13px'}} onClick={async () => {
                        try {
                            const res = await authFetch(`${API_BASE}/pod/restart?id=${pod.id}`, { method: 'POST' });
                            if (res.ok) alert("Pod restart initiated!");
                            else alert("Failed to restart pod: " + await res.text());
                        } catch (e) { alert("Error: " + e.message); }
                    }}>Restart</button>
                    <button style={{background: 'rgba(239, 68, 68, 0.1)', border: '1px solid var(--danger)', color: 'var(--danger)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '13px'}} onClick={async () => {
                        try {
                            const res = await authFetch(`${API_BASE}/pod?id=${pod.id}`, { method: 'DELETE' });
                            if (res.ok) alert("Pod stopped (deleted from cluster)");
                            else alert("Failed to stop pod: " + await res.text());
                        } catch (e) { alert("Error: " + e.message); }
                    }}>Stop</button>
                  </div>
                </div>
              ))}
            </div>
          </>
        )}
        
        {activeTab === 'projects' && (
          <div className="animate-fade-in delay-2">
            <div style={{display: 'flex', justifyContent: 'space-between', marginBottom: '24px', alignItems: 'center'}}>
              <h2 className="section-title" style={{marginBottom: 0}}>Namespaces</h2>
              <button style={{
                background: 'var(--accent-primary)',
                color: 'white',
                border: 'none',
                padding: '10px 16px',
                borderRadius: '8px',
                cursor: 'pointer',
                fontWeight: '600',
                transition: 'all 0.2s'
              }} onClick={() => setShowProjectModal(true)}>
                + Deploy Complex Project
              </button>
            </div>
            
            <div className="dashboard-grid">
              {projects.map((proj) => (
                <div className="glass-card" key={proj.id} style={{display: 'flex', flexDirection: 'column'}}>
                  <div className="card-header" style={{fontSize: '18px', fontWeight: 'bold', color: 'var(--text-primary)', textTransform: 'none'}}>
                    {proj.name}
                  </div>
                  <div style={{marginBottom: '20px'}}>
                    {proj.dir && (
                      <div style={{color: 'var(--text-secondary)', fontSize: '12px', marginBottom: '12px', padding: '8px', background: 'rgba(0,0,0,0.2)', borderRadius: '4px', wordBreak: 'break-all', fontFamily: 'monospace'}}>
                        📁 {proj.dir}
                      </div>
                    )}
                    <div style={{color: 'var(--text-secondary)', fontSize: '14px', marginBottom: '8px', display: 'flex', justifyContent: 'space-between'}}>
                      <span>CPU Quota:</span> <span style={{color: 'var(--text-primary)'}}>{proj.cpuQuota}</span>
                    </div>
                    <div style={{color: 'var(--text-secondary)', fontSize: '14px', marginBottom: '8px', display: 'flex', justifyContent: 'space-between'}}>
                      <span>RAM Quota:</span> <span style={{color: 'var(--text-primary)'}}>{proj.ramQuota}</span>
                    </div>
                    {proj.storageQuota && (
                      <div style={{color: 'var(--text-secondary)', fontSize: '14px', marginBottom: '8px', display: 'flex', justifyContent: 'space-between'}}>
                        <span>Storage:</span> <span style={{color: 'var(--text-primary)'}}>{proj.storageQuota}</span>
                      </div>
                    )}
                    <div style={{color: 'var(--text-secondary)', fontSize: '14px', display: 'flex', justifyContent: 'space-between'}}>
                      <span>Active Pods:</span> <span style={{color: 'var(--text-primary)'}}>{proj.podsCount}</span>
                    </div>
                  </div>
                  <div style={{marginTop: 'auto', paddingTop: '16px', borderTop: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between'}}>
                    <button style={{background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', color: 'var(--text-primary)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer'}}>Edit Quota</button>
                    <button 
                      style={{background: 'rgba(239, 68, 68, 0.1)', border: '1px solid var(--danger)', color: 'var(--danger)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer'}}
                      onClick={() => setProjects(projects.filter(p => p.id !== proj.id))}
                    >
                      Delete
                    </button>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {activeTab === 'converter' && (
          <ComposeConverter apiToken={API_TOKEN()} />
        )}

        {activeTab === 'settings' && (
          <div className="animate-fade-in delay-2">
            <h2 className="section-title">Cluster Settings</h2>
            <div className="pods-section">
              <div style={{marginBottom: '32px'}}>
                <h3 style={{marginBottom: '16px'}}>API Authentication</h3>
                <div className="glass-card">
                  <div style={{marginBottom: '12px'}}>
                    <div style={{fontWeight: 'bold', marginBottom: '4px'}}>API Token</div>
                    <div style={{fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '12px'}}>Token used to authenticate all API requests. Stored locally in your browser.</div>
                  </div>
                  <div style={{display: 'flex', gap: '8px'}}>
                    <input
                      id="settings-api-token"
                      type="password"
                      defaultValue={localStorage.getItem('p2ser_api_token') || ''}
                      placeholder="Enter your API token..."
                      style={{flex: 1, padding: '10px 12px', borderRadius: '8px', background: 'rgba(0,0,0,0.2)', border: '1px solid var(--border-color)', color: 'white', outline: 'none', fontFamily: 'monospace'}}
                    />
                    <button
                      onClick={() => {
                        const val = document.getElementById('settings-api-token').value;
                        localStorage.setItem('p2ser_api_token', val);
                        alert('Token saved! All API requests will now use this token.');
                      }}
                      style={{background: 'var(--accent-primary)', border: 'none', color: 'white', padding: '10px 20px', borderRadius: '8px', cursor: 'pointer', fontWeight: '600'}}
                    >Save</button>
                  </div>
                </div>
              </div>

              <div style={{marginBottom: '32px'}}>
                <h3 style={{marginBottom: '16px'}}>Network & Domains <span style={{fontSize: '12px', background: 'var(--accent-primary)', color: 'white', padding: '2px 6px', borderRadius: '4px', marginLeft: '8px'}}>Coming Soon</span></h3>
                <div className="glass-card" style={{opacity: 0.6}}>
                  <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px'}}>
                    <div>
                      <div style={{fontWeight: 'bold', marginBottom: '4px'}}>Ingress Controller</div>
                      <div style={{fontSize: '13px', color: 'var(--text-secondary)'}}>Handle incoming traffic and route to services</div>
                    </div>
                    <div className="status-badge" style={{background: 'rgba(255,255,255,0.1)', color: 'var(--text-secondary)'}}>Not Configured</div>
                  </div>
                  <button disabled style={{background: 'var(--card-bg)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)', padding: '6px 16px', borderRadius: '6px', cursor: 'not-allowed'}}>Configure Domains</button>
                </div>
              </div>
              
              <div>
                <h3 style={{marginBottom: '16px'}}>Security & Secrets <span style={{fontSize: '12px', background: 'var(--accent-primary)', color: 'white', padding: '2px 6px', borderRadius: '4px', marginLeft: '8px'}}>Coming Soon</span></h3>
                <div className="glass-card" style={{opacity: 0.6}}>
                  <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px'}}>
                    <div>
                      <div style={{fontWeight: 'bold', marginBottom: '4px'}}>Global Environment Variables</div>
                      <div style={{fontSize: '13px', color: 'var(--text-secondary)'}}>Shared secrets across all projects</div>
                    </div>
                    <div style={{fontSize: '13px', color: 'var(--text-secondary)'}}>0 Secrets active</div>
                  </div>
                  <button disabled style={{background: 'var(--card-bg)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)', padding: '6px 16px', borderRadius: '6px', cursor: 'not-allowed'}}>Manage Secrets</button>
                </div>
              </div>
            </div>
          </div>
        )}
      </main>

      {/* Logs Modal */}
      {logsModal.show && (
        <div style={{position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.8)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000}}>
          <div className="glass-card animate-fade-in" style={{width: '80%', maxWidth: '900px', height: '70vh', display: 'flex', flexDirection: 'column'}}>
            <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', paddingBottom: '16px', borderBottom: '1px solid var(--border-color)'}}>
              <h2 style={{margin: 0, fontSize: '18px'}}>{logsModal.title}</h2>
              <button onClick={() => setLogsModal({show: false, title: '', content: ''})} style={{background: 'none', border: 'none', color: 'var(--text-secondary)', cursor: 'pointer', fontSize: '24px'}}>&times;</button>
            </div>
            <div style={{flex: 1, overflowY: 'auto', background: '#0a0a0a', padding: '16px', borderRadius: '0 0 12px 12px', marginTop: '16px', fontFamily: 'monospace', fontSize: '14px', color: '#e5e7eb', whiteSpace: 'pre-wrap'}}>
              {logsModal.content}
            </div>
          </div>
        </div>
      )}

      {/* Terminal Modal */}
      {terminalModal.show && (
        <TerminalWindow 
          podId={terminalModal.podId} 
          podName={terminalModal.podName} 
          onClose={() => setTerminalModal({show: false, podId: '', podName: ''})} 
        />
      )}


      {/* Project Creation Modal */}
      {showProjectModal && (
        <div style={{position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.7)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000}}>
          <div className="glass-card animate-fade-in" style={{width: '500px', background: 'var(--card-bg)', border: '1px solid var(--border-color)', boxShadow: '0 25px 50px -12px rgba(0, 0, 0, 0.5)'}}>
            <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '24px'}}>
              <h2 style={{margin: 0, fontSize: '20px'}}>Deploy Complex Project</h2>
              <button onClick={() => setShowProjectModal(false)} style={{background: 'none', border: 'none', color: 'var(--text-secondary)', cursor: 'pointer', fontSize: '20px'}}>&times;</button>
            </div>
            
            <div style={{display: 'flex', flexDirection: 'column', gap: '16px'}}>
              <div>
                <label style={{display: 'block', marginBottom: '8px', color: 'var(--text-secondary)', fontSize: '14px'}}>Project Name</label>
                <input 
                  type="text" 
                  value={newProjectForm.name}
                  onChange={e => setNewProjectForm({...newProjectForm, name: e.target.value})}
                  placeholder="e.g. My P2SER App"
                  style={{width: '100%', padding: '10px 12px', borderRadius: '8px', background: 'rgba(0,0,0,0.2)', border: '1px solid var(--border-color)', color: 'white', outline: 'none'}}
                />
              </div>

              <div style={{display: 'flex', gap: '10px'}}>
                <button 
                  onClick={() => setNewProjectForm({...newProjectForm, mode: 'folder'})}
                  style={{flex: 1, padding: '10px', background: newProjectForm.mode === 'folder' ? 'var(--primary-color)' : 'rgba(255,255,255,0.1)', color: 'white', border: 'none', borderRadius: '8px', cursor: 'pointer', transition: 'background 0.2s'}}>
                  📁 Upload Folder
                </button>
                <button 
                  onClick={() => setNewProjectForm({...newProjectForm, mode: 'git'})}
                  style={{flex: 1, padding: '10px', background: newProjectForm.mode === 'git' ? 'var(--primary-color)' : 'rgba(255,255,255,0.1)', color: 'white', border: 'none', borderRadius: '8px', cursor: 'pointer', transition: 'background 0.2s'}}>
                  🌐 Git URL
                </button>
              </div>

              <div style={{marginBottom: '16px'}}>
                <label style={{display: 'block', marginBottom: '8px', fontWeight: '500'}}>Environment Variables (.env format) <span style={{color: 'var(--text-secondary)', fontSize: '12px'}}>(Optional)</span></label>
                <textarea 
                  placeholder="DB_PASSWORD=secret&#10;API_KEY=12345"
                  value={newProjectForm.envVars}
                  onChange={(e) => setNewProjectForm({...newProjectForm, envVars: e.target.value})}
                  style={{
                    width: '100%',
                    height: '100px',
                    background: 'rgba(255,255,255,0.03)',
                    border: '1px solid var(--border-color)',
                    borderRadius: '8px',
                    padding: '12px',
                    color: 'var(--text-primary)',
                    fontFamily: 'monospace',
                    resize: 'vertical'
                  }}
                />
              </div>
              
              {newProjectForm.mode === 'folder' ? (
              <div>
                <label style={{display: 'block', marginBottom: '8px', color: 'var(--text-secondary)', fontSize: '14px'}}>Bundle Directory (docker-compose & files)</label>
                <div style={{display: 'flex', gap: '8px'}}>
                  <input 
                    type="text" 
                    value={newProjectForm.dir}
                    onChange={e => setNewProjectForm({...newProjectForm, dir: e.target.value})}
                    placeholder="Select local folder to upload..."
                    style={{flex: 1, padding: '10px 12px', borderRadius: '8px', background: 'rgba(0,0,0,0.2)', border: '1px solid var(--border-color)', color: 'white', outline: 'none'}}
                  />
                  <label style={{background: 'rgba(255,255,255,0.1)', border: '1px solid var(--border-color)', color: 'white', padding: '10px 16px', borderRadius: '8px', cursor: 'pointer', display: 'flex', alignItems: 'center'}}>
                    Browse OS
                    <input 
                      type="file" 
                      webkitdirectory="true" 
                      directory="true" 
                      style={{display: 'none'}}
                      onChange={e => {
                        if (e.target.files && e.target.files.length > 0) {
                          const file = e.target.files[0];
                          const folderName = file.webkitRelativePath.split('/')[0];
                          setNewProjectForm({...newProjectForm, dir: folderName + ' (' + e.target.files.length + ' files)'});
                          setSelectedFiles(Array.from(e.target.files));
                        }
                      }}
                    />
                  </label>
                </div>
              </div>
              ) : (
              <div>
                <label style={{display: 'block', marginBottom: '8px', color: 'var(--text-secondary)', fontSize: '14px'}}>Git Repository URL</label>
                <input 
                  type="text" 
                  value={newProjectForm.gitUrl}
                  onChange={e => setNewProjectForm({...newProjectForm, gitUrl: e.target.value})}
                  placeholder="https://github.com/user/repo.git"
                  style={{width: '100%', padding: '10px 12px', borderRadius: '8px', background: 'rgba(0,0,0,0.2)', border: '1px solid var(--border-color)', color: 'white', outline: 'none', marginBottom: '8px'}}
                />
                <label style={{display: 'block', marginBottom: '8px', color: 'var(--text-secondary)', fontSize: '14px'}}>Branch (optional)</label>
                <input 
                  type="text" 
                  value={newProjectForm.branch}
                  onChange={e => setNewProjectForm({...newProjectForm, branch: e.target.value})}
                  placeholder="main"
                  style={{width: '100%', padding: '10px 12px', borderRadius: '8px', background: 'rgba(0,0,0,0.2)', border: '1px solid var(--border-color)', color: 'white', outline: 'none'}}
                />
              </div>
              )}


            </div>

            <div style={{display: 'flex', justifyContent: 'flex-end', gap: '12px', marginTop: '32px'}}>
              <button 
                onClick={() => setShowProjectModal(false)}
                style={{background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', color: 'var(--text-primary)', padding: '10px 20px', borderRadius: '8px', cursor: 'pointer', fontWeight: '500'}}
              >
                Cancel
              </button>
              <button 
                onClick={async () => {
                  if (newProjectForm.mode === 'git') {
                      if (!newProjectForm.gitUrl) {
                          alert("Please enter a Git URL!");
                          return;
                      }
                      try {
                          const envObj = {};
                          if (newProjectForm.envVars) {
                              newProjectForm.envVars.split('\n').forEach(line => {
                                  const [k, ...vParts] = line.split('=');
                                  if (k && vParts.length > 0) envObj[k.trim()] = vParts.join('=').trim();
                              });
                          }

                          const res = await authFetch(`${API_BASE}/deploy-git?projectName=${encodeURIComponent(newProjectForm.name || 'default')}`, {
                              method: 'POST',
                              headers: {
                                  'Content-Type': 'application/json'
                              },
                              body: JSON.stringify({
                                  url: newProjectForm.gitUrl,
                                  branch: newProjectForm.branch,
                                  env: envObj
                              })
                          });
                          if (res.ok) {
                              alert("Deployment from Git successful!");
                          } else {
                              alert("Deployment from Git failed: " + await res.text());
                          }
                      } catch (e) {
                          alert("Error: " + e.message);
                      }
                  } else {
                      if (selectedFiles.length === 0) {
                          alert("Please select a folder to deploy!");
                          return;
                      }
                      
                      const composeFile = selectedFiles.find(f => f.name === 'docker-compose.yml' || f.name === 'docker-compose.yaml');
                      if (!composeFile) {
                          alert("No docker-compose.yaml found in the selected folder!");
                          return;
                      }
                      
                      try {
                          // Build ZIP file using jszip
                          const zip = new JSZip();
                          const rootDirName = selectedFiles[0].webkitRelativePath.split('/')[0];
                          
                          selectedFiles.forEach(file => {
                              // Remove the root folder name from the path so it zips the contents
                              const relativePath = file.webkitRelativePath.substring(rootDirName.length + 1);
                              if (relativePath) {
                                  zip.file(relativePath, file);
                              }
                          });

                          if (newProjectForm.envVars.trim()) {
                              zip.file('.env', newProjectForm.envVars);
                          }
                          
                          const zipBlob = await zip.generateAsync({type:"blob"});
                          
                          // Send ZIP to /upload
                          const formData = new FormData();
                          formData.append('project', zipBlob, 'project.zip');

                          const res = await authFetch(`${API_BASE}/upload?projectName=${encodeURIComponent(newProjectForm.name || 'default')}`, {
                              method: 'POST',
                              body: formData
                          });
                          
                          if (res.ok) {
                              alert("Folder uploaded and deployment successful!");
                          } else {
                              alert("Deployment failed: " + await res.text());
                          }
                      } catch (e) {
                          alert("Error during packaging: " + e.message);
                      }
                  }
                  
                  setProjects([...projects, { 
                    id: 'proj-' + Date.now(), 
                    name: newProjectForm.name || 'New Project', 
                    dir: newProjectForm.dir,
                    cpuQuota: newProjectForm.cpu + ' Cores', 
                    ramQuota: newProjectForm.ram + ' GB', 
                    storageQuota: newProjectForm.storage + ' GB',
                    podsCount: newProjectForm.replicas 
                  }]);
                  setShowProjectModal(false);
                  setNewProjectForm({name: '', dir: '', cpu: '1', ram: '2', storage: '10', replicas: '1', standby: '0'});
                  setSelectedFiles([]);
                }}
                style={{background: 'var(--accent-primary)', border: 'none', color: 'white', padding: '10px 20px', borderRadius: '8px', cursor: 'pointer', fontWeight: '600'}}
              >
                Deploy Project
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Removed Custom File Browser */}
    </div>
  )
}


// ─────────────────────────────────────────────────────────────────────────────
// Compose → P2SER Converter
// ─────────────────────────────────────────────────────────────────────────────

// Інтерполяція ${VAR:-default} / ${VAR} / $VAR з envMap
function interpolate(str, envMap) {
  if (!str || typeof str !== 'string') return str;
  return str.replace(/\$\{([a-zA-Z_]\w*)(?::?-([^}]*))?\}|\$([a-zA-Z_]\w*)/g, (_, name1, def, name2) => {
    const name = name1 || name2;
    return (envMap && envMap[name]) || def || '';
  });
}

function convertCompose(parsed, serviceSettings, globalSettings, envMap = {}) {
  const result = JSON.parse(JSON.stringify(parsed)); // deep clone
  const services = result.services || {};

  // strip docker-specific top-level keys
  delete result.networks;
  // keep volumes but flatten named volumes refs

  Object.keys(services).forEach(name => {
    const svc = services[name];
    const cfg = serviceSettings[name] || {};

    // remove unsupported fields
    delete svc.container_name;
    delete svc.healthcheck;
    delete svc.networks;
    delete svc.build;

    // інтерполяція image
    if (svc.image) svc.image = interpolate(svc.image, envMap);

    // інтерполяція environment
    if (Array.isArray(svc.environment)) {
      svc.environment = svc.environment.map(e => interpolate(e, envMap));
    } else if (svc.environment && typeof svc.environment === 'object') {
      Object.keys(svc.environment).forEach(k => {
        svc.environment[k] = interpolate(svc.environment[k], envMap);
      });
    }

    // інтерполяція volumes (рядки)
    if (Array.isArray(svc.volumes)) {
      svc.volumes = svc.volumes.map(v => typeof v === 'string' ? interpolate(v, envMap) : v);
    }

    // replicas
    const replicas = parseInt(cfg.replicas ?? globalSettings.replicas ?? 1);
    if (replicas > 0) {
      svc.deploy = svc.deploy || {};
      svc.deploy.replicas = replicas;
    }

    // standby
    const standby = parseInt(cfg.standby ?? globalSettings.standby ?? 0);
    if (standby > 0) svc['x-k1n-standby'] = standby;

    // userns-remap
    const userns = cfg.userns ?? globalSettings.userns ?? false;
    svc['x-userns-remap'] = userns;
  });

  // flatten named volumes to absolute paths where possible
  const namedVols = result.volumes || {};
  Object.keys(services).forEach(name => {
    const svc = services[name];
    if (Array.isArray(svc.volumes)) {
      svc.volumes = svc.volumes.map(v => {
        if (typeof v === 'string' && !v.includes('/') && !v.startsWith('.')) {
          // named volume — check if it has a device binding
          const volDef = namedVols[v.split(':')[0]];
          if (volDef && volDef.driver_opts && volDef.driver_opts.device) {
            const mount = v.split(':').slice(1).join(':');
            return `${volDef.driver_opts.device}:${mount}`;
          }
        }
        return v;
      });
    }
  });

  delete result.volumes; // P2SER doesn't use named volume definitions

  result.services = services;
  return result;
}

function yamlStringify(obj, indent = 0) {
  // Simple YAML serialiser (js-yaml is available in project)
  return yaml.dump(obj, { indent: 2, lineWidth: 120, noRefs: true });
}

function highlightYaml(text) {
  // HTML-escape before injecting into dangerouslySetInnerHTML
  const escaped = text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");

  return escaped
    .replace(/^(#.*)$/gm, '<span style="color:#6b7280">$1</span>')
    .replace(/^([ \t]*)(x-[\w-]+)(:.*)$/gm, '$1<span style="color:#f59e0b">$2</span><span style="color:#a78bfa">$3</span>')
    .replace(/^([ \t]*)(\w[\w-]*)(:.*)$/gm, (_, sp, key, rest) => {
      const keyColor = ['services','image','ports','volumes','environment','depends_on','deploy','command','user'].includes(key)
        ? '#60a5fa' : '#34d399';
      return `${sp}<span style="color:${keyColor}">${key}</span><span style="color:#e5e7eb">${rest}</span>`;
    });
}

// Парсить .env рядок у Map {KEY: VALUE}
function parseEnvText(text) {
  const map = {};
  text.split('\n').forEach(line => {
    line = line.trim();
    if (!line || line.startsWith('#')) return;
    const idx = line.indexOf('=');
    if (idx < 1) return;
    const key = line.slice(0, idx).trim();
    let val = line.slice(idx + 1).trim();
    // знімаємо лапки
    if ((val.startsWith('"') && val.endsWith('"')) ||
        (val.startsWith("'") && val.endsWith("'"))) {
      val = val.slice(1, -1);
    }
    map[key] = val;
  });
  return map;
}

function ComposeConverter({ apiToken }) {
  const [dragOver, setDragOver] = useState(false);
  const [dragOverEnv, setDragOverEnv] = useState(false);
  const [sourceYaml, setSourceYaml] = useState('');
  const [parsed, setParsed] = useState(null);
  const [parseError, setParseError] = useState('');
  const [fileName, setFileName] = useState('');
  const [envText, setEnvText] = useState('');
  const [envFileName, setEnvFileName] = useState('');
  const [showEnvPanel, setShowEnvPanel] = useState(false);
  const [globalSettings, setGlobalSettings] = useState({ replicas: 1, standby: 0, userns: false });
  const [serviceSettings, setServiceSettings] = useState({});
  const [outputYaml, setOutputYaml] = useState('');
  const [deploying, setDeploying] = useState(false);
  const [deployMsg, setDeployMsg] = useState('');
  const fileRef = useRef();
  const envFileRef = useRef();

  const services = parsed ? Object.keys(parsed.services || {}) : [];

  // Re-generate output whenever settings, env or parsed change
  useEffect(() => {
    if (!parsed) { setOutputYaml(''); return; }
    try {
      const converted = convertCompose(parsed, serviceSettings, globalSettings, parseEnvText(envText));
      const header =
        `# P2SER Manifest — конвертовано з ${fileName || 'docker-compose.yaml'}\n` +
        `# Згенеровано: ${new Date().toISOString()}\n\n`;
      setOutputYaml(header + yamlStringify(converted));
    } catch(e) {
      setOutputYaml('# Помилка генерації: ' + e.message);
    }
  }, [parsed, serviceSettings, globalSettings, fileName, envText]);

  function loadYaml(text, name) {
    setFileName(name);
    setSourceYaml(text);
    setDeployMsg('');
    try {
      const p = yaml.load(text);
      if (!p || !p.services) throw new Error("Відсутній розділ 'services'");
      setParsed(p);
      setParseError('');
      // init per-service settings from defaults
      const init = {};
      Object.keys(p.services).forEach(s => { init[s] = { replicas: 1, standby: 0, userns: false }; });
      setServiceSettings(init);
    } catch(e) {
      setParsed(null);
      setParseError(e.message);
    }
  }

  function onFileChange(e) {
    const f = e.target.files[0];
    if (!f) return;
    const reader = new FileReader();
    reader.onload = ev => loadYaml(ev.target.result, f.name);
    reader.readAsText(f);
  }

  function onDrop(e) {
    e.preventDefault(); setDragOver(false);
    const files = Array.from(e.dataTransfer.files);
    // якщо скинули .env — завантажуємо як env
    const envFile = files.find(f => f.name === '.env' || f.name.endsWith('.env'));
    const yamlFile = files.find(f => f.name.match(/\.ya?ml$/i));
    if (envFile) {
      const r = new FileReader();
      r.onload = ev => { setEnvText(ev.target.result); setEnvFileName(envFile.name); setShowEnvPanel(true); };
      r.readAsText(envFile);
    }
    if (yamlFile) {
      const r = new FileReader();
      r.onload = ev => loadYaml(ev.target.result, yamlFile.name);
      r.readAsText(yamlFile);
    }
  }

  function onEnvFileChange(e) {
    const f = e.target.files[0];
    if (!f) return;
    const reader = new FileReader();
    reader.onload = ev => { setEnvText(ev.target.result); setEnvFileName(f.name); setShowEnvPanel(true); };
    reader.readAsText(f);
  }

  function setSvc(name, key, val) {
    setServiceSettings(prev => ({ ...prev, [name]: { ...prev[name], [key]: val } }));
  }

  function downloadManifest() {
    const blob = new Blob([outputYaml], { type: 'text/yaml' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = (fileName || 'docker-compose').replace(/\.(ya?ml)$/, '') + '.p2ser.yaml';
    a.click();
  }

  async function deployManifest() {
    setDeploying(true); setDeployMsg('');
    try {
      const headers = { 'Content-Type': 'application/x-yaml' };
      // передаємо env змінні окремим заголовком
      if (envText.trim()) {
        headers['X-Env-Vars'] = btoa(unescape(encodeURIComponent(envText)));
      }
      const res = await authFetch(`${API_BASE}/compose`, {
        method: 'POST',
        headers,
        body: outputYaml,
      });
      const txt = await res.text();
      if (res.ok) setDeployMsg('✅ ' + (txt || 'Маніфест прийнято кластером!'));
      else setDeployMsg('❌ Помилка: ' + txt);
    } catch(e) {
      setDeployMsg('❌ ' + e.message);
    }
    setDeploying(false);
  }

  const inputStyle = {
    background: 'rgba(0,0,0,0.2)',
    border: '1px solid var(--border-color)',
    borderRadius: '6px',
    color: 'var(--text-primary)',
    padding: '4px 8px',
    outline: 'none',
    width: '70px',
    textAlign: 'center',
  };

  // кількість змінних з .env
  const envVarCount = envText.trim() ? Object.keys(parseEnvText(envText)).length : 0;

  return (
    <div className="animate-fade-in delay-2" style={{ display: 'flex', flexDirection: 'column', gap: '24px' }}>

      {/* Drop zone + env picker row */}
      <div style={{ display: 'flex', gap: '16px', alignItems: 'stretch' }}>

        {/* Compose drop zone */}
        <div
          onDragOver={e => { e.preventDefault(); setDragOver(true); }}
          onDragLeave={() => setDragOver(false)}
          onDrop={onDrop}
          onClick={() => fileRef.current.click()}
          style={{
            flex: 3,
            border: `2px dashed ${dragOver ? 'var(--accent-primary)' : 'var(--border-color)'}`,
            borderRadius: '16px',
            padding: '36px 24px',
            textAlign: 'center',
            cursor: 'pointer',
            background: dragOver ? 'rgba(59,130,246,0.07)' : 'rgba(255,255,255,0.02)',
            transition: 'all 0.2s',
          }}
        >
          <input ref={fileRef} type="file" accept=".yaml,.yml" style={{ display: 'none' }} onChange={onFileChange} />
          <div style={{ fontSize: '40px', marginBottom: '10px' }}>📦</div>
          <div style={{ fontSize: '16px', fontWeight: '600', marginBottom: '6px' }}>
            {fileName ? `✓ ${fileName}` : 'docker-compose.yaml'}
          </div>
          <div style={{ color: 'var(--text-secondary)', fontSize: '13px' }}>перетягни або клікни</div>
        </div>

        {/* .env picker */}
        <div
          onDragOver={e => { e.preventDefault(); setDragOverEnv(true); }}
          onDragLeave={() => setDragOverEnv(false)}
          onDrop={e => {
            e.preventDefault(); setDragOverEnv(false);
            const f = e.dataTransfer.files[0];
            if (!f) return;
            const r = new FileReader();
            r.onload = ev => { setEnvText(ev.target.result); setEnvFileName(f.name); setShowEnvPanel(true); };
            r.readAsText(f);
          }}
          onClick={() => envFileRef.current.click()}
          style={{
            flex: 1,
            border: `2px dashed ${dragOverEnv ? '#f59e0b' : (envVarCount > 0 ? '#f59e0b55' : 'var(--border-color)')}`,
            borderRadius: '16px',
            padding: '36px 16px',
            textAlign: 'center',
            cursor: 'pointer',
            background: envVarCount > 0 ? 'rgba(245,158,11,0.06)' : 'rgba(255,255,255,0.02)',
            transition: 'all 0.2s',
            minWidth: '160px',
          }}
        >
          <input ref={envFileRef} type="file" accept=".env,text/plain" style={{ display: 'none' }} onChange={onEnvFileChange} />
          <div style={{ fontSize: '36px', marginBottom: '10px' }}>🔑</div>
          <div style={{ fontSize: '14px', fontWeight: '600', marginBottom: '6px', color: envVarCount > 0 ? '#f59e0b' : 'var(--text-primary)' }}>
            {envVarCount > 0 ? `${envVarCount} змінних` : '.env файл'}
          </div>
          <div style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
            {envFileName || 'перетягни або клікни'}
          </div>
          {envVarCount > 0 && (
            <div
              onClick={e => { e.stopPropagation(); setShowEnvPanel(p => !p); }}
              style={{ marginTop: '10px', fontSize: '12px', color: '#f59e0b', textDecoration: 'underline', cursor: 'pointer' }}
            >
              {showEnvPanel ? 'сховати' : 'редагувати'}
            </div>
          )}
        </div>
      </div>

      {/* .env editor panel */}
      {showEnvPanel && (
        <div className="glass-card" style={{ borderLeft: '3px solid #f59e0b' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
            <span style={{ fontWeight: '700', fontSize: '14px', color: '#f59e0b' }}>🔑 Змінні середовища (.env)</span>
            <div style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
              <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
                {envVarCount} змінних • підставляються в {'${VAR}'} у compose
              </span>
              <button
                onClick={() => { setEnvText(''); setEnvFileName(''); setShowEnvPanel(false); }}
                style={{ background: 'rgba(239,68,68,0.1)', border: '1px solid var(--danger)', color: 'var(--danger)', padding: '3px 10px', borderRadius: '4px', cursor: 'pointer', fontSize: '12px' }}
              >Очистити</button>
            </div>
          </div>
          <textarea
            value={envText}
            onChange={e => setEnvText(e.target.value)}
            spellCheck={false}
            placeholder={'DB_PASSWORD=secret\nAPI_KEY=abc123\nREDIS_URL=redis://...'}
            style={{
              width: '100%',
              minHeight: '140px',
              background: '#0d1117',
              border: '1px solid rgba(245,158,11,0.3)',
              borderRadius: '8px',
              padding: '12px',
              color: '#fcd34d',
              fontFamily: 'monospace',
              fontSize: '13px',
              lineHeight: '1.6',
              resize: 'vertical',
              outline: 'none',
            }}
          />
          {/* Preview parsed vars */}
          {envVarCount > 0 && (
            <div style={{ marginTop: '10px', display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
              {Object.entries(parseEnvText(envText)).map(([k, v]) => (
                <div key={k} style={{
                  background: 'rgba(245,158,11,0.08)',
                  border: '1px solid rgba(245,158,11,0.2)',
                  borderRadius: '4px',
                  padding: '2px 8px',
                  fontSize: '12px',
                  fontFamily: 'monospace',
                  color: '#fcd34d',
                }}>
                  <span style={{ color: '#f59e0b' }}>{k}</span>
                  <span style={{ color: '#6b7280' }}>=</span>
                  <span style={{ color: '#e5e7eb' }}>{v.length > 20 ? v.slice(0, 20) + '…' : v}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {parseError && (
        <div style={{ background: 'rgba(239,68,68,0.1)', border: '1px solid var(--danger)', borderRadius: '12px', padding: '16px', color: 'var(--danger)', fontFamily: 'monospace', fontSize: '13px' }}>
          ⚠ {parseError}
        </div>
      )}

      {parsed && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '24px' }}>

          {/* Left: settings */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>

            {/* Global settings */}
            <div className="glass-card">
              <div style={{ fontWeight: '700', marginBottom: '16px', fontSize: '14px', color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>🌐 Глобальні налаштування</div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: '14px' }}>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                  <span style={{ fontSize: '14px' }}>Replicas (всі сервіси)</span>
                  <input
                    type="number" min="1" max="20" value={globalSettings.replicas}
                    onChange={e => setGlobalSettings(p => ({ ...p, replicas: +e.target.value }))}
                    style={inputStyle}
                  />
                </div>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                  <span style={{ fontSize: '14px' }}>x-k1n-standby (резерв)</span>
                  <input
                    type="number" min="0" max="10" value={globalSettings.standby}
                    onChange={e => setGlobalSettings(p => ({ ...p, standby: +e.target.value }))}
                    style={inputStyle}
                  />
                </div>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                  <span style={{ fontSize: '14px' }}>x-userns-remap</span>
                  <label style={{ display: 'flex', alignItems: 'center', gap: '8px', cursor: 'pointer' }}>
                    <input type="checkbox" checked={globalSettings.userns}
                      onChange={e => setGlobalSettings(p => ({ ...p, userns: e.target.checked }))}
                      style={{ width: '16px', height: '16px', accentColor: 'var(--accent-primary)' }}
                    />
                    <span style={{ fontSize: '13px', color: 'var(--text-secondary)' }}>{globalSettings.userns ? 'увімкнено' : 'вимкнено'}</span>
                  </label>
                </div>
              </div>
            </div>

            {/* Per-service settings */}
            {services.map(name => (
              <div className="glass-card" key={name} style={{ borderLeft: '3px solid var(--accent-primary)' }}>
                <div style={{ fontWeight: '700', marginBottom: '14px', fontSize: '15px', color: 'var(--accent-primary)' }}>
                  🔧 {name}
                  <span style={{ marginLeft: '10px', fontSize: '12px', color: 'var(--text-secondary)', fontWeight: '400' }}>
                    {parsed.services[name].image || '(build)'}
                  </span>
                </div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                    <span style={{ fontSize: '13px', color: 'var(--text-secondary)' }}>replicas</span>
                    <input
                      type="number" min="1" max="20"
                      value={serviceSettings[name]?.replicas ?? globalSettings.replicas}
                      onChange={e => setSvc(name, 'replicas', +e.target.value)}
                      style={inputStyle}
                    />
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                    <span style={{ fontSize: '13px', color: 'var(--text-secondary)' }}>x-k1n-standby</span>
                    <input
                      type="number" min="0" max="10"
                      value={serviceSettings[name]?.standby ?? globalSettings.standby}
                      onChange={e => setSvc(name, 'standby', +e.target.value)}
                      style={inputStyle}
                    />
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                    <span style={{ fontSize: '13px', color: 'var(--text-secondary)' }}>x-userns-remap</span>
                    <label style={{ display: 'flex', alignItems: 'center', gap: '8px', cursor: 'pointer' }}>
                      <input type="checkbox"
                        checked={serviceSettings[name]?.userns ?? globalSettings.userns}
                        onChange={e => setSvc(name, 'userns', e.target.checked)}
                        style={{ width: '16px', height: '16px', accentColor: 'var(--accent-primary)' }}
                      />
                      <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
                        {(serviceSettings[name]?.userns ?? globalSettings.userns) ? 'on' : 'off'}
                      </span>
                    </label>
                  </div>
                  {/* What's stripped */}
                  <div style={{ marginTop: '4px', padding: '8px', background: 'rgba(0,0,0,0.2)', borderRadius: '6px', fontSize: '11px', color: 'var(--text-secondary)', fontFamily: 'monospace' }}>
                    {['build','container_name','healthcheck','networks'].filter(k => parsed.services[name][k] !== undefined).map(k =>
                      <div key={k} style={{ color: '#ef4444' }}>— {k}: <span style={{opacity:0.6}}>видалено</span></div>
                    )}
                    {['build','container_name','healthcheck','networks'].filter(k => parsed.services[name][k] !== undefined).length === 0 &&
                      <div style={{ color: '#34d399' }}>✓ сумісний</div>
                    }
                  </div>
                </div>
              </div>
            ))}
          </div>

          {/* Right: output */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
            <div className="glass-card" style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
                <span style={{ fontWeight: '700', fontSize: '14px', color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>📄 P2SER Маніфест</span>
                <div style={{ display: 'flex', gap: '8px' }}>
                  <button
                    onClick={downloadManifest}
                    style={{ background: 'rgba(255,255,255,0.07)', border: '1px solid var(--border-color)', color: 'var(--text-primary)', padding: '6px 14px', borderRadius: '6px', cursor: 'pointer', fontSize: '13px' }}
                  >⬇ Скачати</button>
                  <button
                    onClick={deployManifest}
                    disabled={deploying}
                    style={{ background: deploying ? 'rgba(59,130,246,0.3)' : 'var(--accent-primary)', border: 'none', color: 'white', padding: '6px 14px', borderRadius: '6px', cursor: deploying ? 'not-allowed' : 'pointer', fontWeight: '600', fontSize: '13px', transition: 'all 0.2s' }}
                  >{deploying ? '⏳ Деплой...' : '🚀 Deploy у кластер'}</button>
                </div>
              </div>
              <pre style={{
                flex: 1,
                overflowY: 'auto',
                background: '#0d1117',
                borderRadius: '10px',
                padding: '16px',
                fontFamily: 'monospace',
                fontSize: '13px',
                lineHeight: '1.6',
                color: '#e5e7eb',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-all',
                minHeight: '400px',
                border: '1px solid rgba(255,255,255,0.06)',
              }}
                dangerouslySetInnerHTML={{ __html: highlightYaml(outputYaml) }}
              />
            </div>

            {deployMsg && (
              <div style={{
                padding: '14px 18px',
                borderRadius: '10px',
                background: deployMsg.startsWith('✅') ? 'rgba(52,211,153,0.1)' : 'rgba(239,68,68,0.1)',
                border: `1px solid ${deployMsg.startsWith('✅') ? '#34d399' : 'var(--danger)'}`,
                fontFamily: 'monospace',
                fontSize: '14px',
                color: deployMsg.startsWith('✅') ? '#34d399' : 'var(--danger)',
              }}>
                {deployMsg}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function TerminalWindow({ podId, podName, onClose }) {
  const terminalRef = useRef(null);
  const term = useRef(null);
  const socket = useRef(null);

  useEffect(() => {
    term.current = new Terminal({
      theme: { background: '#000000', foreground: '#ffffff' },
      cursorBlink: true,
      fontFamily: 'monospace',
    });
    const fitAddon = new FitAddon();
    term.current.loadAddon(fitAddon);
    
    term.current.open(terminalRef.current);
    fitAddon.fit();

    // Connect WebSocket via Ticket (H-12)
    authFetch(`${API_BASE}/ticket`, { method: 'POST' }).then(res => {
      if (res.ok) return res.json();
      throw new Error("Ticket request failed");
    }).then(data => {
      const wsUrl = `${API_WS_BASE}/pod/exec?id=${podId}&ticket=${data.ticket}`;
      socket.current = new WebSocket(wsUrl);

      socket.current.onopen = () => {
        term.current.writeln(`\x1b[32m*** Connected to ${podName} ***\x1b[0m`);
      };

      socket.current.onmessage = (event) => {
        term.current.write(event.data);
      };

      socket.current.onclose = () => {
        term.current.writeln('\r\n\x1b[31m*** Connection closed ***\x1b[0m');
      };

      term.current.onData((data) => {
        if (socket.current.readyState === WebSocket.OPEN) {
          socket.current.send(data);
        }
      });
    }).catch(err => {
      term.current.writeln(`\r\n\x1b[31m*** Failed to connect: ${err.message} ***\x1b[0m`);
    });
    const handleResize = () => fitAddon.fit();
    window.addEventListener('resize', handleResize);

    return () => {
      window.removeEventListener('resize', handleResize);
      if (socket.current) socket.current.close();
      if (term.current) term.current.dispose();
    };
  }, [podId, podName]);

  return (
    <div style={{position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.8)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000}}>
      <div className="glass-card animate-fade-in" style={{width: '80%', maxWidth: '1000px', height: '80vh', display: 'flex', flexDirection: 'column'}}>
        <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', paddingBottom: '16px', borderBottom: '1px solid var(--border-color)'}}>
          <h2 style={{margin: 0, fontSize: '18px'}}>Terminal: {podName}</h2>
          <button onClick={onClose} style={{background: 'none', border: 'none', color: 'var(--text-secondary)', cursor: 'pointer', fontSize: '24px'}}>&times;</button>
        </div>
        <div style={{flex: 1, marginTop: '16px', background: '#000', borderRadius: '8px', overflow: 'hidden'}} ref={terminalRef}></div>
      </div>
    </div>
  );
}
