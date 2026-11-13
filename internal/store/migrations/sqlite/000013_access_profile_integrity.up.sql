CREATE TRIGGER managed_host_profile_insert_guard
BEFORE INSERT ON managed_hosts
WHEN NEW.access_profile_id <> '' AND NOT EXISTS (
    SELECT 1 FROM access_profiles p
    WHERE p.workspace_id = NEW.workspace_id AND p.id = NEW.access_profile_id AND p.deleted_at IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'managed Host Access Profile must exist in the same workspace');
END;

CREATE TRIGGER managed_host_profile_update_guard
BEFORE UPDATE OF workspace_id, access_profile_id ON managed_hosts
WHEN NEW.access_profile_id <> '' AND NOT EXISTS (
    SELECT 1 FROM access_profiles p
    WHERE p.workspace_id = NEW.workspace_id AND p.id = NEW.access_profile_id AND p.deleted_at IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'managed Host Access Profile must exist in the same workspace');
END;

CREATE TRIGGER access_profile_revision_update_guard
BEFORE UPDATE ON access_profile_revisions
BEGIN
    SELECT RAISE(ABORT, 'Access Profile revisions are immutable');
END;

CREATE TRIGGER access_profile_revision_delete_guard
BEFORE DELETE ON access_profile_revisions
BEGIN
    SELECT RAISE(ABORT, 'Access Profile revisions are retained');
END;

CREATE TRIGGER access_profile_delete_reference_guard
BEFORE UPDATE OF deleted_at ON access_profiles
WHEN NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL AND EXISTS (
    SELECT 1 FROM managed_hosts h WHERE h.workspace_id = OLD.workspace_id AND h.access_profile_id = OLD.id
)
BEGIN
    SELECT RAISE(ABORT, 'referenced Access Profiles cannot be deleted');
END;
