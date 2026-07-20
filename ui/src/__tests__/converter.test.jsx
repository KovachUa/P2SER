/**
 * P2SER UI — Compose Converter Logic Tests
 *
 * Перевіряє чисту бізнес-логіку конвертації docker-compose → P2SER:
 * інтерполяцію змінних, обробку сервісів, replicas, volumes, env.
 */
import { describe, it, expect } from 'vitest'

// Import pure functions directly from App.jsx
// We need to extract them. Since they're module-level functions, we re-implement them here
// for pure unit testing (they don't depend on React).

// ── Re-exported from App.jsx logic ──

function interpolate(str, envMap) {
  if (!str || typeof str !== 'string') return str;
  return str.replace(/\$\{([a-zA-Z_]\w*)(?::?-([^}]*))?\}|\$([a-zA-Z_]\w*)/g, (_, name1, def, name2) => {
    const name = name1 || name2;
    return (envMap && envMap[name]) || def || '';
  });
}

function parseEnvText(text) {
  const map = {};
  text.split('\n').forEach(line => {
    line = line.trim();
    if (!line || line.startsWith('#')) return;
    const idx = line.indexOf('=');
    if (idx < 1) return;
    const key = line.slice(0, idx).trim();
    let val = line.slice(idx + 1).trim();
    if ((val.startsWith('"') && val.endsWith('"')) ||
        (val.startsWith("'") && val.endsWith("'"))) {
      val = val.slice(1, -1);
    }
    map[key] = val;
  });
  return map;
}

function convertCompose(parsed, serviceSettings, globalSettings, envMap = {}) {
  const result = JSON.parse(JSON.stringify(parsed));
  const services = result.services || {};

  delete result.networks;

  Object.keys(services).forEach(name => {
    const svc = services[name];
    const cfg = serviceSettings[name] || {};

    delete svc.container_name;
    delete svc.healthcheck;
    delete svc.networks;
    delete svc.build;

    if (svc.image) svc.image = interpolate(svc.image, envMap);

    if (Array.isArray(svc.environment)) {
      svc.environment = svc.environment.map(e => interpolate(e, envMap));
    } else if (svc.environment && typeof svc.environment === 'object') {
      Object.keys(svc.environment).forEach(k => {
        svc.environment[k] = interpolate(svc.environment[k], envMap);
      });
    }

    if (Array.isArray(svc.volumes)) {
      svc.volumes = svc.volumes.map(v => typeof v === 'string' ? interpolate(v, envMap) : v);
    }

    const replicas = parseInt(cfg.replicas ?? globalSettings.replicas ?? 1);
    if (replicas > 0) {
      svc.deploy = svc.deploy || {};
      svc.deploy.replicas = replicas;
    }

    const standby = parseInt(cfg.standby ?? globalSettings.standby ?? 0);
    if (standby > 0) svc['x-k1n-standby'] = standby;

    const userns = cfg.userns ?? globalSettings.userns ?? false;
    svc['x-userns-remap'] = userns;
  });

  const namedVols = result.volumes || {};
  Object.keys(services).forEach(name => {
    const svc = services[name];
    if (Array.isArray(svc.volumes)) {
      svc.volumes = svc.volumes.map(v => {
        if (typeof v === 'string' && !v.includes('/') && !v.startsWith('.')) {
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

  delete result.volumes;
  result.services = services;
  return result;
}


// ── Tests ──

describe('interpolate()', () => {
  it('should replace ${VAR} with value from envMap', () => {
    expect(interpolate('${DB_HOST}', { DB_HOST: 'pg.local' })).toBe('pg.local')
  })

  it('should replace $VAR with value from envMap', () => {
    expect(interpolate('$DB_HOST', { DB_HOST: 'pg.local' })).toBe('pg.local')
  })

  it('should use default value when VAR is missing: ${VAR:-default}', () => {
    expect(interpolate('${DB_HOST:-localhost}', {})).toBe('localhost')
  })

  it('should return empty string when VAR is missing and no default', () => {
    expect(interpolate('${MISSING}', {})).toBe('')
  })

  it('should handle multiple variables in one string', () => {
    const env = { HOST: 'db', PORT: '5432' }
    expect(interpolate('${HOST}:${PORT}', env)).toBe('db:5432')
  })

  it('should return non-string values unchanged', () => {
    expect(interpolate(null, {})).toBeNull()
    expect(interpolate(undefined, {})).toBeUndefined()
    expect(interpolate(123, {})).toBe(123)
  })

  it('should handle string without variables unchanged', () => {
    expect(interpolate('hello world', {})).toBe('hello world')
  })
})

describe('parseEnvText()', () => {
  it('should parse simple KEY=VALUE pairs', () => {
    expect(parseEnvText('DB_HOST=localhost\nDB_PORT=5432')).toEqual({
      DB_HOST: 'localhost',
      DB_PORT: '5432',
    })
  })

  it('should strip double quotes from values', () => {
    expect(parseEnvText('KEY="value"')).toEqual({ KEY: 'value' })
  })

  it('should strip single quotes from values', () => {
    expect(parseEnvText("KEY='value'")).toEqual({ KEY: 'value' })
  })

  it('should skip comments', () => {
    expect(parseEnvText('# this is a comment\nKEY=val')).toEqual({ KEY: 'val' })
  })

  it('should skip empty lines', () => {
    expect(parseEnvText('\n\nKEY=val\n\n')).toEqual({ KEY: 'val' })
  })

  it('should handle values with = sign', () => {
    expect(parseEnvText('URL=postgres://user:pass@host/db?opt=1')).toEqual({
      URL: 'postgres://user:pass@host/db?opt=1',
    })
  })

  it('should return empty object for empty input', () => {
    expect(parseEnvText('')).toEqual({})
  })
})

describe('convertCompose()', () => {
  const baseParsed = {
    version: '3.8',
    services: {
      web: {
        image: 'nginx:${TAG:-latest}',
        ports: ['80:80'],
        environment: ['DB_HOST=${DB_HOST:-db}'],
        container_name: 'web-container',
        networks: ['default'],
      },
      db: {
        image: 'postgres:15',
        volumes: ['pgdata:/var/lib/postgresql/data'],
        environment: { POSTGRES_PASSWORD: '${PG_PASS:-secret}' },
        healthcheck: { test: ['CMD', 'pg_isready'] },
      },
    },
    networks: { default: {} },
    volumes: { pgdata: {} },
  }

  it('should remove docker-specific keys (networks, container_name, healthcheck)', () => {
    const result = convertCompose(baseParsed, {}, { replicas: 1, standby: 0, userns: false })
    
    expect(result.networks).toBeUndefined()
    expect(result.volumes).toBeUndefined()
    expect(result.services.web.container_name).toBeUndefined()
    expect(result.services.web.networks).toBeUndefined()
    expect(result.services.db.healthcheck).toBeUndefined()
  })

  it('should interpolate image names with env vars', () => {
    const result = convertCompose(baseParsed, {}, { replicas: 1, standby: 0, userns: false }, { TAG: 'v2' })
    expect(result.services.web.image).toBe('nginx:v2')
  })

  it('should use default value when env var is missing', () => {
    const result = convertCompose(baseParsed, {}, { replicas: 1, standby: 0, userns: false }, {})
    expect(result.services.web.image).toBe('nginx:latest')
  })

  it('should interpolate environment variables (array format)', () => {
    const result = convertCompose(baseParsed, {}, { replicas: 1, standby: 0, userns: false }, { DB_HOST: 'prod-db' })
    expect(result.services.web.environment).toContain('DB_HOST=prod-db')
  })

  it('should interpolate environment variables (map format)', () => {
    const result = convertCompose(baseParsed, {}, { replicas: 1, standby: 0, userns: false }, { PG_PASS: 'super-secret' })
    expect(result.services.db.environment.POSTGRES_PASSWORD).toBe('super-secret')
  })

  it('should add deploy.replicas from global settings', () => {
    const result = convertCompose(baseParsed, {}, { replicas: 3, standby: 0, userns: false })
    expect(result.services.web.deploy.replicas).toBe(3)
    expect(result.services.db.deploy.replicas).toBe(3)
  })

  it('should override replicas per-service', () => {
    const result = convertCompose(baseParsed, { web: { replicas: 5 } }, { replicas: 1, standby: 0, userns: false })
    expect(result.services.web.deploy.replicas).toBe(5)
    expect(result.services.db.deploy.replicas).toBe(1)
  })

  it('should add x-k1n-standby when standby > 0', () => {
    const result = convertCompose(baseParsed, {}, { replicas: 1, standby: 2, userns: false })
    expect(result.services.web['x-k1n-standby']).toBe(2)
  })

  it('should NOT add x-k1n-standby when standby is 0', () => {
    const result = convertCompose(baseParsed, {}, { replicas: 1, standby: 0, userns: false })
    expect(result.services.web['x-k1n-standby']).toBeUndefined()
  })

  it('should set x-userns-remap based on global setting', () => {
    const result = convertCompose(baseParsed, {}, { replicas: 1, standby: 0, userns: true })
    expect(result.services.web['x-userns-remap']).toBe(true)
    expect(result.services.db['x-userns-remap']).toBe(true)
  })

  it('should not mutate the original parsed object', () => {
    const original = JSON.parse(JSON.stringify(baseParsed))
    convertCompose(baseParsed, {}, { replicas: 1, standby: 0, userns: false })
    expect(baseParsed).toEqual(original)
  })

  it('should keep named volumes without driver_opts unchanged', () => {
    const parsed = {
      services: {
        db: { image: 'postgres', volumes: ['mydata:/data'] },
      },
      volumes: {
        mydata: {},
      },
    }
    const result = convertCompose(parsed, {}, { replicas: 1, standby: 0, userns: false })
    expect(result.services.db.volumes).toContain('mydata:/data')
  })
})
