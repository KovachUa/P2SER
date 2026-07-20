export const getAuthHeaders = () => {
  // Use encodeURIComponent to prevent fetch crash if user enters Cyrillic token
  const token = encodeURIComponent(localStorage.getItem('p2ser_token') || '')
  return {
    'Authorization': `Bearer ${token}`
  }
}

export const getEndpoint = () => {
  const ep = localStorage.getItem('p2ser_endpoint');
  return ep ? ep.replace(/\/$/, '') : '';
}

export async function fetchPods() {
  const url = `${getEndpoint()}/pods`;
  const res = await fetch(url, {
    method: 'GET',
    headers: getAuthHeaders()
  });
  if (!res.ok) throw new Error(`HTTP Error: ${res.status}`);
  return await res.json();
}

export async function fetchNodes() {
  const url = `${getEndpoint()}/nodes`;
  const res = await fetch(url, {
    method: 'GET',
    headers: getAuthHeaders()
  });
  if (!res.ok) throw new Error(`HTTP Error: ${res.status}`);
  return await res.json();
}

export async function fetchState(key) {
  const url = `${getEndpoint()}/state?key=${encodeURIComponent(key)}`;
  const res = await fetch(url, {
    method: 'GET',
    headers: getAuthHeaders()
  });
  if (!res.ok) {
      if (res.status === 404) return null;
      throw new Error(`HTTP Error: ${res.status}`);
  }
  return await res.text();
}

export async function deployCompose(yamlText, projectName = 'default') {
  const url = `${getEndpoint()}/compose`;
  const res = await fetch(url, {
    method: 'POST',
    headers: {
        ...getAuthHeaders(),
        'Content-Type': 'application/x-yaml',
        'X-Project-Name': projectName
    },
    body: yamlText
  });
  if (!res.ok) throw new Error(`Deploy failed: ${res.status} - ${await res.text()}`);
  return await res.text();
}

export async function banIP(ip) {
  const url = `${getEndpoint()}/ban?ip=${encodeURIComponent(ip)}`;
  const res = await fetch(url, {
    method: 'POST',
    headers: getAuthHeaders()
  });
  if (!res.ok) throw new Error(`Ban failed: ${res.status} - ${await res.text()}`);
  return await res.text();
}
