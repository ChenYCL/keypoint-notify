package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/ChenYCL/keypoint-notify/internal/model"
)

// KeyPrefix is the literal prefix of every API key this system issues.
const KeyPrefix = "kp_"

// NewAPIKey mints a fresh API key. The plaintext is returned once and never
// stored; only its SHA-256 lands in the database.
func NewAPIKey() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return KeyPrefix + lowerASCII(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// HashKey returns the storage form of an API key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

// KeyDisplay is the short form shown in listings and logs.
func KeyDisplay(key string) string {
	if len(key) <= 10 {
		return key
	}
	return key[:10] + "…"
}

// ---------------------------------------------------------------------------
// Identities
// ---------------------------------------------------------------------------

const identityCols = `id, name, kind, roles, active_role, key_prefix, disabled, created_at, updated_at`

func scanIdentity(row interface{ Scan(...any) error }) (model.Identity, error) {
	var (
		id        model.Identity
		ms        int64
		ums       int64
		dis       int
		raw       string
		keyPrefix string
	)
	// key_hash is intentionally never selected by identityCols.
	err := row.Scan(&id.ID, &id.Name, &id.Kind, &raw, &id.ActiveRole, &keyPrefix, &dis, &ms, &ums)
	if err != nil {
		return model.Identity{}, err
	}
	id.Roles = scanStrings(raw)
	id.KeyPrefix = keyPrefix
	id.Disabled = dis != 0
	id.CreatedAt = msToTime(ms)
	id.UpdatedAt = msToTime(ums)
	return id, nil
}

// CreateIdentity inserts a new identity and returns it along with the
// plaintext API key (shown to the caller exactly once).
func (s *Store) CreateIdentity(name, kind string, roles []string, activeRole string) (model.Identity, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Identity{}, "", errors.New("identity name is required")
	}
	if kind != model.KindHuman && kind != model.KindAgent {
		kind = model.KindAgent
	}
	if len(roles) == 0 {
		roles = []string{"member"}
	}
	if activeRole == "" || !containsString(roles, activeRole) {
		activeRole = roles[0]
	}
	key := NewAPIKey()
	now := nowMS()
	id := model.Identity{
		ID:         NewID("idn_"),
		Name:       name,
		Kind:       kind,
		Roles:      roles,
		ActiveRole: activeRole,
		KeyPrefix:  KeyDisplay(key),
		CreatedAt:  msToTime(now),
		UpdatedAt:  msToTime(now),
	}
	err := s.tx(func(tx *sql.Tx) error {
		for _, r := range roles {
			if err := ensureRoleTx(tx, r); err != nil {
				return err
			}
		}
		_, err := tx.Exec(
			`INSERT INTO identities(id, name, kind, roles, active_role, key_hash, key_prefix, disabled, created_at, updated_at)
			 VALUES(?,?,?,?,?,?,?,0,?,?)`,
			id.ID, id.Name, id.Kind, jsonStrings(roles), activeRole, HashKey(key), id.KeyPrefix, now, now)
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			return model.Identity{}, "", fmt.Errorf("%w: identity %q already exists", ErrConflict, name)
		}
		return model.Identity{}, "", err
	}
	return id, key, nil
}

// IdentityByKey resolves an API key to its identity. Returns ErrNotFound for
// unknown keys and disabled identities alike, so a caller cannot probe for
// which names exist.
func (s *Store) IdentityByKey(key string) (model.Identity, error) {
	row := s.db.QueryRow(
		`SELECT `+identityCols+` FROM identities WHERE key_hash = ? AND disabled = 0`, HashKey(key))
	id, err := scanIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Identity{}, ErrNotFound
	}
	return id, err
}

// IdentityByName looks up an identity by its unique name.
func (s *Store) IdentityByName(name string) (model.Identity, error) {
	row := s.db.QueryRow(`SELECT `+identityCols+` FROM identities WHERE name = ?`, name)
	id, err := scanIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Identity{}, ErrNotFound
	}
	return id, err
}

// IdentityByID looks up an identity by primary key.
func (s *Store) IdentityByID(id string) (model.Identity, error) {
	row := s.db.QueryRow(`SELECT `+identityCols+` FROM identities WHERE id = ?`, id)
	out, err := scanIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Identity{}, ErrNotFound
	}
	return out, err
}

// identityByNameTx is the transaction-scoped form of IdentityByName. Code that
// already holds a transaction must use this rather than the *Store method:
// borrowing a second pooled connection while a write transaction is open can
// deadlock under concurrency.
func identityByNameTx(tx *sql.Tx, name string) (model.Identity, error) {
	row := tx.QueryRow(`SELECT `+identityCols+` FROM identities WHERE name = ?`, name)
	out, err := scanIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Identity{}, ErrNotFound
	}
	return out, err
}

// ListIdentities returns every identity, oldest first.
func (s *Store) ListIdentities() ([]model.Identity, error) {
	rows, err := s.db.Query(`SELECT ` + identityCols + ` FROM identities ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Identity{}
	for rows.Next() {
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CountIdentities reports how many identities exist. Used to gate bootstrap.
func (s *Store) CountIdentities() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM identities`).Scan(&n)
	return n, err
}

// UpdateIdentity applies a patch to an identity. Only non-nil fields change.
// Changing roles re-validates active_role so an identity can never point at a
// role it does not hold.
func (s *Store) UpdateIdentity(id string, name *string, kind *string, roles *[]string, activeRole *string, disabled *bool) (model.Identity, error) {
	cur, err := s.IdentityByID(id)
	if err != nil {
		return model.Identity{}, err
	}
	if name != nil && strings.TrimSpace(*name) != "" {
		cur.Name = strings.TrimSpace(*name)
	}
	if kind != nil && (*kind == model.KindHuman || *kind == model.KindAgent) {
		cur.Kind = *kind
	}
	if roles != nil {
		if len(*roles) == 0 {
			return model.Identity{}, errors.New("identity must keep at least one role")
		}
		cur.Roles = *roles
	}
	if activeRole != nil && *activeRole != "" {
		if !containsString(cur.Roles, *activeRole) {
			return model.Identity{}, fmt.Errorf("identity %q does not hold role %q", cur.Name, *activeRole)
		}
		cur.ActiveRole = *activeRole
	}
	if !containsString(cur.Roles, cur.ActiveRole) {
		cur.ActiveRole = cur.Roles[0]
	}
	if disabled != nil {
		cur.Disabled = *disabled
	}
	now := nowMS()
	cur.UpdatedAt = msToTime(now)
	err = s.tx(func(tx *sql.Tx) error {
		for _, r := range cur.Roles {
			if err := ensureRoleTx(tx, r); err != nil {
				return err
			}
		}
		_, err := tx.Exec(
			`UPDATE identities SET name=?, kind=?, roles=?, active_role=?, disabled=?, updated_at=? WHERE id=?`,
			cur.Name, cur.Kind, jsonStrings(cur.Roles), cur.ActiveRole, boolToInt(cur.Disabled), now, id)
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			return model.Identity{}, fmt.Errorf("%w: identity %q already exists", ErrConflict, cur.Name)
		}
		return model.Identity{}, err
	}
	return cur, nil
}

// RotateKey mints a new API key for an identity and invalidates the old one
// immediately. Returns the new plaintext key.
func (s *Store) RotateKey(id string) (string, error) {
	if _, err := s.IdentityByID(id); err != nil {
		return "", err
	}
	key := NewAPIKey()
	_, err := s.db.Exec(
		`UPDATE identities SET key_hash=?, key_prefix=?, updated_at=? WHERE id=?`,
		HashKey(key), KeyDisplay(key), nowMS(), id)
	if err != nil {
		return "", err
	}
	return key, nil
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

// BuiltinRoles are seeded on first boot. They are deliberately few and generic:
// a role is a routing address, not an org chart.
func BuiltinRoles() []model.Role {
	return []model.Role{
		{Key: "member", Name: "成员", Description: "通用参与角色，可接任意工作面", DefaultSides: []string{"general"}, Builtin: true},
		{Key: "backend", Name: "后端", Description: "服务端、数据、接口实现", Capabilities: []string{"api", "db", "service"}, DefaultSides: []string{"api", "backend"}, Builtin: true},
		{Key: "frontend", Name: "前端", Description: "界面、交互、组件实现", Capabilities: []string{"ui", "component", "style"}, DefaultSides: []string{"ui", "frontend"}, Builtin: true},
		{Key: "review", Name: "审查", Description: "代码审查、方案对抗、风险挑刺", Capabilities: []string{"review", "audit"}, DefaultSides: []string{"review"}, Builtin: true},
		{Key: "qa", Name: "测试", Description: "测试用例、端到端验证、回归", Capabilities: []string{"test", "e2e", "regression"}, DefaultSides: []string{"test", "qa"}, Builtin: true},
		{Key: "ops", Name: "运维", Description: "部署、CI/CD、线上问题处理", Capabilities: []string{"deploy", "ci", "infra"}, DefaultSides: []string{"ops", "deploy"}, Builtin: true},
		{Key: "design", Name: "设计", Description: "视觉、交互设计、设计稿解读", Capabilities: []string{"design", "figma", "ux"}, DefaultSides: []string{"design"}, Builtin: true},
	}
}

// SeedRoles inserts the builtin roles if the table is empty.
func (s *Store) SeedRoles() error {
	return s.tx(func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM roles`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		for _, r := range BuiltinRoles() {
			if err := insertRoleTx(tx, r); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertRoleTx(tx *sql.Tx, r model.Role) error {
	_, err := tx.Exec(
		`INSERT INTO roles(key, name, description, capabilities, default_sides, builtin, created_at)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(key) DO UPDATE SET name=excluded.name, description=excluded.description`,
		r.Key, r.Name, r.Description, jsonStrings(r.Capabilities), jsonStrings(r.DefaultSides),
		boolToInt(r.Builtin), nowMS())
	return err
}

// ensureRoleTx creates a placeholder role if the key is unknown, so that
// assigning work to a brand-new role never fails.
func ensureRoleTx(tx *sql.Tx, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	_, err := tx.Exec(
		`INSERT INTO roles(key, name, description, capabilities, default_sides, builtin, created_at)
		 VALUES(?,?,?,'[]','[]',0,?)
		 ON CONFLICT(key) DO NOTHING`,
		key, key, "自定义角色", nowMS())
	return err
}

// ListRoles returns all roles, builtins first.
func (s *Store) ListRoles() ([]model.Role, error) {
	rows, err := s.db.Query(
		`SELECT key, name, description, capabilities, default_sides, builtin FROM roles
		 ORDER BY builtin DESC, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Role{}
	for rows.Next() {
		var (
			r       model.Role
			caps    string
			sides   string
			builtin int
		)
		if err := rows.Scan(&r.Key, &r.Name, &r.Description, &caps, &sides, &builtin); err != nil {
			return nil, err
		}
		r.Capabilities = scanStrings(caps)
		r.DefaultSides = scanStrings(sides)
		r.Builtin = builtin != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertRole creates or updates a custom role.
func (s *Store) UpsertRole(r model.Role) error {
	r.Key = strings.TrimSpace(r.Key)
	if r.Key == "" {
		return errors.New("role key is required")
	}
	if r.Name == "" {
		r.Name = r.Key
	}
	return s.tx(func(tx *sql.Tx) error { return insertRoleTx(tx, r) })
}

// DeleteRole removes a custom role. Builtin roles are protected.
func (s *Store) DeleteRole(key string) error {
	res, err := s.db.Exec(`DELETE FROM roles WHERE key = ? AND builtin = 0`, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: role %q is builtin or missing", ErrNotFound, key)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// AdminCount reports how many enabled identities hold the admin role.
//
// The API refuses role changes that need admin without admin, which means a
// system whose last admin key is lost is unrecoverable through the API. The
// count exists so bootstrap can detect that state and let the operator back in.
func (s *Store) AdminCount() (int, error) {
	ids, err := s.ListIdentities()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, idn := range ids {
		if idn.HasRole("admin") && !idn.Disabled {
			n++
		}
	}
	return n, nil
}
