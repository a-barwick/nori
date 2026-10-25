-- Local-only roles use trust auth on the isolated control bridge.
CREATE ROLE bot LOGIN;
CREATE ROLE temporal LOGIN;
CREATE DATABASE bot OWNER bot;
CREATE DATABASE temporal OWNER temporal;
CREATE DATABASE temporal_visibility OWNER temporal;
REVOKE CONNECT ON DATABASE bot FROM PUBLIC;
REVOKE CONNECT ON DATABASE temporal FROM PUBLIC;
REVOKE CONNECT ON DATABASE temporal_visibility FROM PUBLIC;
