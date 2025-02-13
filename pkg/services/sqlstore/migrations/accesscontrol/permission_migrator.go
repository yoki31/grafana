package accesscontrol

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"xorm.io/xorm"

	"github.com/grafana/grafana/pkg/services/accesscontrol"
	"github.com/grafana/grafana/pkg/services/sqlstore/migrator"
	"github.com/grafana/grafana/pkg/util"
)

var (
	batchSize = 500
)

type permissionMigrator struct {
	sess    *xorm.Session
	dialect migrator.Dialect
	migrator.MigrationBase
}

func (m *permissionMigrator) SQL(dialect migrator.Dialect) string {
	return "code migration"
}

func (m *permissionMigrator) findRole(orgID int64, name string) (accesscontrol.Role, error) {
	// check if role exists
	var role accesscontrol.Role
	_, err := m.sess.Table("role").Where("org_id = ? AND name = ?", orgID, name).Get(&role)
	return role, err
}

func (m *permissionMigrator) bulkCreateRoles(allRoles []*accesscontrol.Role) ([]*accesscontrol.Role, error) {
	if len(allRoles) == 0 {
		return nil, nil
	}

	allCreatedRoles := make([]*accesscontrol.Role, 0, len(allRoles))

	createRoles := m.createRoles
	if m.dialect.DriverName() == migrator.MySQL {
		createRoles = m.createRolesMySQL
	}

	// bulk role creations
	err := batch(len(allRoles), batchSize, func(start, end int) error {
		roles := allRoles[start:end]
		createdRoles, err := createRoles(roles, start, end)
		if err != nil {
			return err
		}
		allCreatedRoles = append(allCreatedRoles, createdRoles...)
		return nil
	})

	return allCreatedRoles, err
}

func (m *permissionMigrator) bulkAssignRoles(rolesMap map[int64]map[string]*accesscontrol.Role, assignments map[int64]map[string]struct{}) error {
	if len(assignments) == 0 {
		return nil
	}

	ts := time.Now()
	userRoleAssignments := make([]accesscontrol.UserRole, 0)
	teamRoleAssignments := make([]accesscontrol.TeamRole, 0)
	builtInRoleAssignments := make([]accesscontrol.BuiltinRole, 0)

	for orgID, roleNames := range assignments {
		for name := range roleNames {
			role, ok := rolesMap[orgID][name]
			if !ok {
				return &ErrUnknownRole{name}
			}

			if strings.HasPrefix(name, "managed:users") {
				userID, err := strconv.ParseInt(strings.Split(name, ":")[2], 10, 64)
				if err != nil {
					return err
				}
				userRoleAssignments = append(userRoleAssignments, accesscontrol.UserRole{
					OrgID:   role.OrgID,
					RoleID:  role.ID,
					UserID:  userID,
					Created: ts,
				})
			} else if strings.HasPrefix(name, "managed:teams") {
				teamID, err := strconv.ParseInt(strings.Split(name, ":")[2], 10, 64)
				if err != nil {
					return err
				}
				teamRoleAssignments = append(teamRoleAssignments, accesscontrol.TeamRole{
					OrgID:   role.OrgID,
					RoleID:  role.ID,
					TeamID:  teamID,
					Created: ts,
				})
			} else if strings.HasPrefix(name, "managed:builtins") {
				builtIn := strings.Title(strings.Split(name, ":")[2])
				builtInRoleAssignments = append(builtInRoleAssignments, accesscontrol.BuiltinRole{
					OrgID:   role.OrgID,
					RoleID:  role.ID,
					Role:    builtIn,
					Created: ts,
					Updated: ts,
				})
			}
		}
	}

	err := batch(len(userRoleAssignments), batchSize, func(start, end int) error {
		_, err := m.sess.Table("user_role").InsertMulti(userRoleAssignments[start:end])
		return err
	})
	if err != nil {
		return err
	}

	err = batch(len(teamRoleAssignments), batchSize, func(start, end int) error {
		_, err := m.sess.Table("team_role").InsertMulti(teamRoleAssignments[start:end])
		return err
	})
	if err != nil {
		return err
	}

	return batch(len(builtInRoleAssignments), batchSize, func(start, end int) error {
		_, err := m.sess.Table("builtin_role").InsertMulti(builtInRoleAssignments[start:end])
		return err
	})
}

// createRoles creates a list of roles and returns their id, orgID, name in a single query
func (m *permissionMigrator) createRoles(roles []*accesscontrol.Role, start int, end int) ([]*accesscontrol.Role, error) {
	ts := time.Now()
	createdRoles := make([]*accesscontrol.Role, 0, len(roles))
	valueStrings := make([]string, len(roles))
	args := make([]interface{}, 0, len(roles)*5)

	for i, r := range roles {
		uid, err := generateNewRoleUID(m.sess, r.OrgID)
		if err != nil {
			return nil, err
		}

		valueStrings[i] = "(?, ?, ?, 1, ?, ?)"
		args = append(args, r.OrgID, uid, r.Name, ts, ts)
	}

	// Insert and fetch at once
	valueString := strings.Join(valueStrings, ",")
	sql := fmt.Sprintf("INSERT INTO role (org_id, uid, name, version, created, updated) VALUES %s RETURNING id, org_id, name", valueString)
	if errCreate := m.sess.SQL(sql, args...).Find(&createdRoles); errCreate != nil {
		return nil, errCreate
	}

	return createdRoles, nil
}

// createRolesMySQL creates a list of roles then fetches them
func (m *permissionMigrator) createRolesMySQL(roles []*accesscontrol.Role, start int, end int) ([]*accesscontrol.Role, error) {
	ts := time.Now()
	createdRoles := make([]*accesscontrol.Role, 0, len(roles))

	where := make([]string, len(roles))
	args := make([]interface{}, 0, len(roles)*2)

	for i := range roles {
		uid, err := generateNewRoleUID(m.sess, roles[i].OrgID)
		if err != nil {
			return nil, err
		}

		roles[i].UID = uid
		roles[i].Created = ts
		roles[i].Updated = ts

		where[i] = ("(org_id = ? AND uid = ?)")
		args = append(args, roles[i].OrgID, uid)
	}

	// Insert roles
	if _, errCreate := m.sess.Table("role").Insert(&roles); errCreate != nil {
		return nil, errCreate
	}

	// Fetch newly created roles
	if errFindInsertions := m.sess.Table("role").
		Where(strings.Join(where, " OR "), args...).
		Find(&createdRoles); errFindInsertions != nil {
		return nil, errFindInsertions
	}

	return createdRoles, nil
}

func batch(count, batchSize int, eachFn func(start, end int) error) error {
	for i := 0; i < count; {
		end := i + batchSize
		if end > count {
			end = count
		}

		if err := eachFn(i, end); err != nil {
			return err
		}

		i = end
	}

	return nil
}

func generateNewRoleUID(sess *xorm.Session, orgID int64) (string, error) {
	for i := 0; i < 3; i++ {
		uid := util.GenerateShortUID()

		exists, err := sess.Where("org_id=? AND uid=?", orgID, uid).Get(&accesscontrol.Role{})
		if err != nil {
			return "", err
		}

		if !exists {
			return uid, nil
		}
	}

	return "", fmt.Errorf("failed to generate uid")
}
