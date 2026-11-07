CREATE TABLE mcp_servers_global (
 id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, transport TEXT NOT NULL DEFAULT 'stdio',
 command TEXT NOT NULL DEFAULT '', args_json TEXT NOT NULL DEFAULT '[]', env_enc TEXT NOT NULL DEFAULT '',
 url TEXT NOT NULL DEFAULT '', headers_enc TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
INSERT INTO mcp_servers_global (id,name,transport,command,args_json,env_enc,url,headers_enc,enabled,created_at,updated_at)
 SELECT id, CASE WHEN row_number() OVER (PARTITION BY name ORDER BY created_at, id) = 1 THEN name
 ELSE name || ' [' || id || ']' END, transport,command,args_json,env_enc,url,headers_enc,enabled,created_at,updated_at
 FROM mcp_servers;
DROP TABLE mcp_servers;
ALTER TABLE mcp_servers_global RENAME TO mcp_servers;
