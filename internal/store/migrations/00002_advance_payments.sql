-- +goose Up
-- Allow the explicitly authorized point purge to remove advance payment events.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION deny_event_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' AND current_setting('meterya.purge_point',true)=OLD.aggregate_id::text
 AND OLD.event_type IN ('PointCreated','PointRenamed','PointDetailsSaved','PointArchived','PointRestored','ReadingRecorded','ReadingCorrected','ReadingRetracted','ReadingPhotoAttached','TariffScheduleSaved','AdvancePaymentScheduleSaved','MonthlyStatementsImported','PointWorkspaceMoved')
 THEN RETURN OLD; END IF;
 RAISE EXCEPTION 'events are immutable';
END; $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION deny_event_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' AND current_setting('meterya.purge_point',true)=OLD.aggregate_id::text
 AND OLD.event_type IN ('PointCreated','PointRenamed','PointDetailsSaved','PointArchived','PointRestored','ReadingRecorded','ReadingCorrected','ReadingRetracted','ReadingPhotoAttached','TariffScheduleSaved','MonthlyStatementsImported','PointWorkspaceMoved')
 THEN RETURN OLD; END IF;
 RAISE EXCEPTION 'events are immutable';
END; $$;
-- +goose StatementEnd
