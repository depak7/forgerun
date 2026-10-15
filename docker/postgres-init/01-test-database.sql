-- A separate database for the Postgres tests, which empty tables as they
-- run. Runs only when the data volume is first created; on an existing one:
--   docker compose exec -T postgres psql -U forgerun -c 'CREATE DATABASE forgerun_test'
CREATE DATABASE forgerun_test;
