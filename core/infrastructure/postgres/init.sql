-- Local-only roles use trust auth on the isolated control bridge.
CREATE ROLE nori LOGIN;
CREATE ROLE temporal LOGIN;
CREATE DATABASE nori OWNER nori;
CREATE DATABASE temporal OWNER temporal;
CREATE DATABASE temporal_visibility OWNER temporal;
REVOKE CONNECT ON DATABASE nori FROM PUBLIC;
REVOKE CONNECT ON DATABASE temporal FROM PUBLIC;
REVOKE CONNECT ON DATABASE temporal_visibility FROM PUBLIC;
