package store

// A persisted ADMIN grant applies system-wide. All other roles are scoped to
// their workspace. Keep the underlying row locked during modifying commands.
const roleQuery = `SELECT role FROM members WHERE active AND subject=$2 AND (workspace_id=$1 OR role='ADMIN') AND EXISTS(SELECT 1 FROM workspaces WHERE id=$1) ORDER BY CASE WHEN role='ADMIN' THEN 0 ELSE 1 END LIMIT 1`

func CanManage(role string) bool { return role == "ADMIN" || role == "MODERATOR" }
func ValidRole(role string) bool { return CanManage(role) || role == "USER" || role == "GUEST" }
