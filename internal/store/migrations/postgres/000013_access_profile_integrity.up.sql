CREATE FUNCTION machinarchy_guard_managed_host_profile() RETURNS trigger AS $$
BEGIN
    IF NEW.access_profile_id <> '' AND NOT EXISTS (
        SELECT 1 FROM access_profiles p
        WHERE p.workspace_id = NEW.workspace_id AND p.id = NEW.access_profile_id AND p.deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'managed Host Access Profile must exist in the same workspace';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER managed_host_profile_guard
BEFORE INSERT OR UPDATE OF workspace_id, access_profile_id ON managed_hosts
FOR EACH ROW EXECUTE FUNCTION machinarchy_guard_managed_host_profile();

CREATE FUNCTION machinarchy_reject_access_profile_revision_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'Access Profile revisions are immutable';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER access_profile_revision_update_guard
BEFORE UPDATE OR DELETE ON access_profile_revisions
FOR EACH ROW EXECUTE FUNCTION machinarchy_reject_access_profile_revision_mutation();

CREATE FUNCTION machinarchy_guard_access_profile_delete() RETURNS trigger AS $$
BEGIN
    IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL AND EXISTS (
        SELECT 1 FROM managed_hosts h WHERE h.workspace_id = OLD.workspace_id AND h.access_profile_id = OLD.id
    ) THEN
        RAISE EXCEPTION 'referenced Access Profiles cannot be deleted';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER access_profile_delete_reference_guard
BEFORE UPDATE OF deleted_at ON access_profiles
FOR EACH ROW EXECUTE FUNCTION machinarchy_guard_access_profile_delete();
