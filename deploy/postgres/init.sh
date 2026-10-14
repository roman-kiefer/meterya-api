#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
  --set=app_password="$APP_DB_PASSWORD" --set=kc_password="$KC_DB_PASSWORD" <<'SQL'
CREATE ROLE meterya LOGIN PASSWORD :'app_password';
CREATE DATABASE meterya OWNER meterya;
CREATE ROLE keycloak LOGIN PASSWORD :'kc_password';
CREATE DATABASE keycloak OWNER keycloak;
SQL
