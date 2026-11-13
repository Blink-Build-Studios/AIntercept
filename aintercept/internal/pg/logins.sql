-- The two logins (docs/PIPELINE.md §3.1, §6), run once by an admin, connected to the database
-- they use (built-in C.UTF-8 locale). Safe to re-run: it sets the passwords again.
--   psql: psql -v worker_password=… -v api_password=… -f logins.sql   (Compose's initdb.sh)
--   Go:   pg.CreateLogins
-- aintercept_worker owns the database and the schema it applies (pg.Apply); aintercept_api can
-- only read. The passwords are never written anywhere but the server and the components' DSNs.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'aintercept_worker') THEN
    CREATE ROLE aintercept_worker LOGIN;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'aintercept_api') THEN
    CREATE ROLE aintercept_api LOGIN;
  END IF;
END
$$;
ALTER ROLE aintercept_worker LOGIN PASSWORD :'worker_password';
ALTER ROLE aintercept_api LOGIN PASSWORD :'api_password';

-- Settings per login: a session idle in a transaction, a lock wait and a statement are bounded.
ALTER ROLE aintercept_worker SET idle_in_transaction_session_timeout = '30s';
ALTER ROLE aintercept_worker SET lock_timeout = '5s';
ALTER ROLE aintercept_worker SET statement_timeout = '60s';
ALTER ROLE aintercept_api SET idle_in_transaction_session_timeout = '30s';
ALTER ROLE aintercept_api SET statement_timeout = '30s';

-- The worker owns the database, and so its public schema and every table it creates; the API
-- reads every table, now and later.
DO $$
BEGIN
  EXECUTE format('ALTER DATABASE %I OWNER TO aintercept_worker', current_database());
  EXECUTE format('REVOKE ALL ON DATABASE %I FROM PUBLIC', current_database());
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO aintercept_api', current_database());
END
$$;
ALTER SCHEMA public OWNER TO aintercept_worker;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO aintercept_api;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO aintercept_api;
ALTER DEFAULT PRIVILEGES FOR ROLE aintercept_worker IN SCHEMA public GRANT SELECT ON TABLES TO aintercept_api;
