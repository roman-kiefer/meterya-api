-- +goose Up
-- First release schema. Development databases from before this baseline are unsupported.
CREATE TABLE release_schema (version integer PRIMARY KEY CHECK(version = 1));
INSERT INTO release_schema(version) VALUES (1);
CREATE TABLE workspaces (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  timezone text NOT NULL DEFAULT 'Europe/Berlin',
  version bigint NOT NULL DEFAULT 1
);
CREATE TABLE members (
  workspace_id uuid REFERENCES workspaces,
  subject text NOT NULL,
  email text NOT NULL,
  role text NOT NULL CHECK(role IN ('ADMIN','MODERATOR','USER','GUEST')),
  active boolean NOT NULL DEFAULT true,
  PRIMARY KEY(workspace_id,subject)
);
CREATE TABLE events (
  id uuid PRIMARY KEY,
  sequence bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
  aggregate_id uuid NOT NULL,
  aggregate_version bigint NOT NULL,
  workspace_id uuid NOT NULL REFERENCES workspaces,
  event_type text NOT NULL,
  actor text NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  schema_version integer NOT NULL DEFAULT 1,
  payload jsonb NOT NULL,
  UNIQUE(aggregate_id,aggregate_version)
);
CREATE INDEX events_workspace_sequence ON events(workspace_id,sequence DESC);
CREATE INDEX events_workspace_type_sequence ON events(workspace_id,event_type,sequence DESC);
CREATE INDEX events_type_sequence ON events(event_type,sequence DESC);
-- +goose StatementBegin
CREATE FUNCTION deny_event_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' AND current_setting('meterya.purge_point',true)=OLD.aggregate_id::text
 AND OLD.event_type IN ('PointCreated','PointRenamed','PointDetailsSaved','PointArchived','PointRestored','ReadingRecorded','ReadingCorrected','ReadingRetracted','ReadingPhotoAttached','TariffScheduleSaved','MonthlyStatementsImported','PointWorkspaceMoved')
 THEN RETURN OLD; END IF;
 RAISE EXCEPTION 'events are immutable';
END; $$;
CREATE TRIGGER events_immutable BEFORE UPDATE OR DELETE ON events FOR EACH ROW EXECUTE FUNCTION deny_event_mutation();
-- +goose StatementEnd
CREATE TABLE points (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces,
  serial text NOT NULL,
  version bigint NOT NULL,
  state jsonb NOT NULL,
  archived boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX points_active_serial ON points(workspace_id,serial) WHERE NOT archived;
CREATE TABLE readings (
  id uuid PRIMARY KEY,
  point_id uuid NOT NULL REFERENCES points,
  register_id uuid NOT NULL,
  reading_date date NOT NULL,
  value numeric(20,6) NOT NULL CHECK(value >= 0),
  actor text NOT NULL,
  recorded_at timestamptz NOT NULL,
  UNIQUE(register_id,reading_date)
);
CREATE TABLE annual_summaries (
  point_id uuid NOT NULL REFERENCES points(id) ON DELETE CASCADE,
  year integer NOT NULL,
  point_version bigint NOT NULL,
  calculation_version integer NOT NULL,
  state jsonb NOT NULL,
  PRIMARY KEY(point_id,year)
);
CREATE TABLE idempotency (
  subject text NOT NULL,
  key uuid NOT NULL,
  fingerprint text NOT NULL,
  response jsonb NOT NULL,
  PRIMARY KEY(subject,key)
);
CREATE TABLE sessions (
  token_hash text PRIMARY KEY,
  subject text NOT NULL,
  email text NOT NULL,
  acr text NOT NULL,
  csrf text NOT NULL,
  expires_at timestamptz NOT NULL,
  last_seen timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE oidc_states (
  state_hash text PRIMARY KEY,
  verifier text NOT NULL,
  nonce text NOT NULL,
  return_to text NOT NULL,
  expires_at timestamptz NOT NULL
);
CREATE TABLE invitations (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces,
  email text NOT NULL,
  role text NOT NULL CHECK(role IN ('ADMIN','MODERATOR','USER','GUEST')),
  token_hash text NOT NULL UNIQUE,
  status text NOT NULL CHECK(status IN ('PENDING','SENT','FAILED','ACCEPTED','REVOKED')),
  expires_at timestamptz NOT NULL,
  actor text NOT NULL,
  subject text,
  version bigint NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX invitations_open_email ON invitations(workspace_id,email) WHERE status IN ('PENDING','SENT','FAILED');
CREATE TABLE virtual_points (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces,
  version bigint NOT NULL,
  state jsonb NOT NULL,
  archived boolean NOT NULL DEFAULT false,
  deleted boolean NOT NULL DEFAULT false
);
CREATE INDEX virtual_points_workspace ON virtual_points(workspace_id) WHERE NOT deleted;
CREATE TABLE photos (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces,
 point_id uuid REFERENCES points ON DELETE CASCADE,
 reading_id uuid,
 owner text NOT NULL,
 original bytea NOT NULL,
 preview bytea,
 edited bytea CHECK(edited IS NULL OR octet_length(edited) BETWEEN 1 AND 20971520),
 media_type text NOT NULL CHECK(media_type IN ('image/jpeg','image/png','image/webp')),
 size bigint NOT NULL CHECK(size BETWEEN 1 AND 20971520),
 sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),
 width integer NOT NULL CHECK(width>0),
 height integer NOT NULL CHECK(height>0 AND width::bigint*height<=50000000),
 captured_date text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz,
 document jsonb NOT NULL,
 CHECK(octet_length(original)=size),
 CHECK((point_id IS NULL AND reading_id IS NULL AND expires_at IS NOT NULL) OR
       (point_id IS NOT NULL AND reading_id IS NOT NULL AND expires_at IS NULL))
);
CREATE INDEX photos_point_reading ON photos(point_id,reading_id);
CREATE INDEX photos_expiring ON photos(expires_at) WHERE point_id IS NULL;
-- +goose StatementBegin
CREATE FUNCTION deny_photo_original_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.original IS DISTINCT FROM OLD.original OR NEW.sha256 IS DISTINCT FROM OLD.sha256 OR NEW.size IS DISTINCT FROM OLD.size OR NEW.media_type IS DISTINCT FROM OLD.media_type THEN
  RAISE EXCEPTION 'photo originals are immutable';
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER photos_immutable_original BEFORE UPDATE ON photos FOR EACH ROW EXECUTE FUNCTION deny_photo_original_change();
CREATE FUNCTION deny_photo_edit_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.point_id IS NOT NULL AND (NEW.edited IS DISTINCT FROM OLD.edited OR NEW.document->'edits' IS DISTINCT FROM OLD.document->'edits' OR NEW.document->'edited_sha256' IS DISTINCT FROM OLD.document->'edited_sha256') THEN
  RAISE EXCEPTION 'photo edits are immutable after confirmation';
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER photos_immutable_edit BEFORE UPDATE ON photos FOR EACH ROW EXECUTE FUNCTION deny_photo_edit_change();
-- +goose StatementEnd
