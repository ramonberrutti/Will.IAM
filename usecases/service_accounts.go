package usecases

import (
	"context"
	"fmt"

	"github.com/gofrs/uuid"
	"github.com/topfreegames/Will.IAM/errors"
	"github.com/topfreegames/Will.IAM/models"
	"github.com/topfreegames/Will.IAM/oauth2"
	"github.com/topfreegames/Will.IAM/repositories"
)

// ServiceAccounts define entrypoints for ServiceAccount actions
type ServiceAccounts interface {
	AuthenticateAccessToken(string) (*models.AccessTokenAuth, error)
	AuthenticateKeyPair(string, string) (string, error)
	Create(*models.ServiceAccount) error
	CreateKeyPairType(string) (*models.ServiceAccount, error)
	CreateOAuth2Type(string, string) (*models.ServiceAccount, error)
	CreatePermission(string, *models.Permission) error
	CreateWithNested(*ServiceAccountWithNested) error
	ForEmail(string) (*models.ServiceAccount, error)
	Get(string) (*models.ServiceAccount, error)
	GetPermissions(string) ([]models.Permission, error)
	GetRoles(string) ([]models.Role, error)
	GetWithNested(string) (*ServiceAccountWithNested, error)
	HasAllOwnerPermissions(string, []models.Permission) (bool, error)
	HasAllOwnerRolesPermissions(string, []string) (bool, error)
	HasPermissionString(string, string) (bool, error)
	HasPermissions(string, []models.Permission) ([]bool, error)
	HasPermissionsStrings(string, []string) ([]bool, error)
	List(*repositories.ListOptions) ([]models.ServiceAccount, int64, error)
	ListWithPermission(
		string,
		*repositories.ListOptions, models.Permission,
	) ([]models.ServiceAccount, int64, error)
	UpdateWithNested(*ServiceAccountWithNested) error
	Search(
		string, *repositories.ListOptions,
	) ([]models.ServiceAccount, int64, error)
	WithContext(context.Context) ServiceAccounts
}

type serviceAccounts struct {
	repo           *repositories.All
	ctx            context.Context
	oauth2Provider oauth2.Provider
}

func (sas serviceAccounts) WithContext(ctx context.Context) ServiceAccounts {
	return &serviceAccounts{
		sas.repo.WithContext(ctx), ctx, sas.oauth2Provider.WithContext(ctx),
	}
}

// NewServiceAccounts serviceAccounts ctor
func NewServiceAccounts(
	repo *repositories.All,
	provider oauth2.Provider,
) ServiceAccounts {
	return &serviceAccounts{
		repo:           repo,
		oauth2Provider: provider,
	}
}

// ServiceAccountWithNested is the required data to update a role
type ServiceAccountWithNested struct {
	ID                 string                    `json:"id"`
	Name               string                    `json:"name"`
	Email              string                    `json:"email"`
	Picture            string                    `json:"picture"`
	PermissionsStrings []string                  `json:"permissions"`
	PermissionsAliases map[string]string         `json:"permissionsAliases"`
	Permissions        []models.Permission       `json:"-"`
	RolesIDs           []string                  `json:"rolesIds,omitempty"`
	Roles              []models.Role             `json:"roles"`
	AuthenticationType models.AuthenticationType `json:"authenticationType"`
}

// Validate ServiceAccountWithNested fields
func (sawn ServiceAccountWithNested) Validate() models.Validation {
	v := &models.Validation{}
	if sawn.Name == "" {
		v.AddError("name", "required")
	}
	if !sawn.AuthenticationType.Valid() {
		v.AddError("authenticatonType", "must be oauth2 or keypair")
	}
	if sawn.AuthenticationType == models.AuthenticationTypes.OAuth2 &&
		sawn.Email == "" {
		v.AddError("email", "required")
	}
	return *v
}

func (sas serviceAccounts) CreateWithNested(
	sawn *ServiceAccountWithNested,
) error {
	return sas.repo.WithPGTx(sas.ctx, func(repo *repositories.All) error {
		var sa *models.ServiceAccount
		if sawn.AuthenticationType == models.AuthenticationTypes.OAuth2 {
			sa = models.BuildOAuth2ServiceAccount(sawn.Name, sawn.Email)
			if err := createServiceAccount(sa, repo); err != nil {
				return err
			}
		} else if sawn.AuthenticationType == models.AuthenticationTypes.KeyPair {
			sa = models.BuildKeyPairServiceAccount(sawn.Name)
			if err := createServiceAccount(sa, repo); err != nil {
				return err
			}
		}
		sawn.ID = sa.ID
		for i := range sawn.RolesIDs {
			if err := repo.Roles.Bind(&models.RoleBinding{
				ServiceAccountID: sawn.ID,
				RoleID:           sawn.RolesIDs[i],
			}); err != nil {
				return err
			}
		}
		for i := range sawn.Permissions {
			sawn.Permissions[i].RoleID = sa.BaseRoleID
			if err := repo.Permissions.Create(&sawn.Permissions[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (sas serviceAccounts) UpdateWithNested(
	sawn *ServiceAccountWithNested,
) error {
	return sas.repo.WithPGTx(sas.ctx, func(repo *repositories.All) error {
		sa, err := repo.ServiceAccounts.Get(sawn.ID)
		if err != nil {
			return err
		}
		sa.Name = sawn.Name
		sa.Email = sawn.Name
		if err := repo.ServiceAccounts.Update(sa); err != nil {
			return err
		}
		if err := repo.ServiceAccounts.DropBindings(sawn.ID); err != nil {
			return err
		}
		for _, roleID := range sawn.RolesIDs {
			if roleID == sa.BaseRoleID {
				continue
			}
			if err := repo.Roles.Bind(&models.RoleBinding{
				ServiceAccountID: sa.ID,
				RoleID:           roleID,
			}); err != nil {
				return err
			}
		}
		if err := repo.Roles.DropPermissions(sa.BaseRoleID); err != nil {
			return err
		}
		for i := range sawn.Permissions {
			sawn.Permissions[i].RoleID = sa.BaseRoleID
			if err := repo.Permissions.Create(&sawn.Permissions[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetWithNested returns a service account by id with permissions and roles
func (sas serviceAccounts) GetWithNested(
	serviceAccountID string,
) (*ServiceAccountWithNested, error) {
	sa, err := sas.repo.ServiceAccounts.Get(serviceAccountID)
	if err != nil {
		return nil, err
	}
	pSl, err := sas.repo.Permissions.ForRole(sa.BaseRoleID)
	if err != nil {
		return nil, err
	}
	permissionsAliases := map[string]string{}
	permissions := make([]string, len(pSl))
	for i := range pSl {
		str := pSl[i].String()
		permissions[i] = str
		if pSl[i].Alias != "" {
			permissionsAliases[str] = pSl[i].Alias
		}
	}
	roles, err := sas.repo.Roles.ForServiceAccountID(serviceAccountID)
	if err != nil {
		return nil, err
	}
	return &ServiceAccountWithNested{
		ID:                 sa.ID,
		Name:               sa.Name,
		Email:              sa.Email,
		Picture:            sa.Picture,
		Roles:              roles,
		AuthenticationType: sa.AuthenticationType,
		PermissionsStrings: permissions,
		PermissionsAliases: permissionsAliases,
	}, nil
}

func (sas serviceAccounts) HasAllOwnerRolesPermissions(
	saID string, rolesIDs []string,
) (bool, error) {
	ps := []models.Permission{}
	for _, roleID := range rolesIDs {
		rps, err := sas.repo.Permissions.ForRole(roleID)
		if err != nil {
			return false, err
		}
		ps = append(ps, rps...)
	}
	return sas.HasAllOwnerPermissions(saID, ps)
}

func (sas serviceAccounts) Create(sa *models.ServiceAccount) error {
	return sas.repo.WithPGTx(sas.ctx, func(repo *repositories.All) error {
		return createServiceAccount(sa, repo)
	})
}

func createServiceAccount(
	sa *models.ServiceAccount, repo *repositories.All,
) error {
	sa.ID = uuid.Must(uuid.NewV4()).String()
	r := &models.Role{
		Name:       fmt.Sprintf("service-account:%s", sa.ID),
		IsBaseRole: true,
	}
	if err := repo.Roles.Create(r); err != nil {
		return err
	}
	sa.BaseRoleID = r.ID
	if err := repo.ServiceAccounts.Create(sa); err != nil {
		return err
	}
	if err := repo.Roles.Bind(&models.RoleBinding{
		RoleID:           r.ID,
		ServiceAccountID: sa.ID,
	}); err != nil {
		return err
	}
	return nil
}

// CreateKeyPairType will build a random key pair and create a
// Service Account with it
func (sas serviceAccounts) CreateKeyPairType(
	saName string,
) (*models.ServiceAccount, error) {
	saKP := models.BuildKeyPairServiceAccount(saName)
	if err := sas.Create(saKP); err != nil {
		return nil, err
	}
	return saKP, nil
}

// CreateOAuth2Type creates an oauth2 service account
func (sas serviceAccounts) CreateOAuth2Type(
	saName, saEmail string,
) (*models.ServiceAccount, error) {
	saOAuth2 := models.BuildOAuth2ServiceAccount(saName, saEmail)
	if err := sas.Create(saOAuth2); err != nil {
		return nil, err
	}
	return saOAuth2, nil
}

// GetRoles returns all roles to which the serviceAccountID is bound to
func (sas serviceAccounts) GetRoles(
	serviceAccountID string,
) ([]models.Role, error) {
	roles, err := sas.repo.Roles.ForServiceAccountID(serviceAccountID)
	if err != nil {
		return nil, err
	}
	return roles, nil
}

// Get returns a service account by id
func (sas serviceAccounts) Get(
	serviceAccountID string,
) (*models.ServiceAccount, error) {
	sa, err := sas.repo.ServiceAccounts.Get(serviceAccountID)
	if err != nil {
		return nil, err
	}
	return sa, nil
}

// ForEmail returns a service account by email
func (sas serviceAccounts) ForEmail(
	email string,
) (*models.ServiceAccount, error) {
	return sas.repo.ServiceAccounts.ForEmail(email)
}

// List returns a list of all service accounts
func (sas serviceAccounts) List(
	lo *repositories.ListOptions,
) ([]models.ServiceAccount, int64, error) {
	saSl, err := sas.repo.ServiceAccounts.List(lo)
	if err != nil {
		return nil, 0, err
	}
	count, err := sas.repo.ServiceAccounts.ListCount()
	if err != nil {
		return nil, 0, err
	}
	return saSl, count, nil
}

// ListWithPermissions returns a list of all service accounts with permission
// either through base role or any other role
func (sas serviceAccounts) ListWithPermission(
	serviceAccountID string, lo *repositories.ListOptions, permission models.Permission,
) ([]models.ServiceAccount, int64, error) {
	rootPermission := permission
	rootPermission.OwnershipLevel = models.OwnershipLevels.Owner

	has, err := sas.repo.ServiceAccounts.HasPermission(serviceAccountID, rootPermission)
	if err != nil {
		return nil, 0, err
	}

	if !has {
		return nil, 0, errors.NewUserDoesntHavePermissionError(rootPermission.String())
	}

	saSl, err := sas.repo.ServiceAccounts.ListWithPermission(lo, permission)
	if err != nil {
		return nil, 0, err
	}
	count, err := sas.repo.ServiceAccounts.ListWithPermissionCount(permission)
	if err != nil {
		return nil, 0, err
	}
	return saSl, count, nil
}

// Search over Service Accounts names and emails
func (sas serviceAccounts) Search(
	term string, lo *repositories.ListOptions,
) ([]models.ServiceAccount, int64, error) {
	saSl, err := sas.repo.ServiceAccounts.Search(term, lo)
	if err != nil {
		return nil, 0, err
	}
	count, err := sas.repo.ServiceAccounts.SearchCount(term)
	if err != nil {
		return nil, 0, err
	}
	return saSl, count, nil
}

// AuthenticateAccessToken verifies if token is valid for email, and sometimes refreshes it
func (sas *serviceAccounts) AuthenticateAccessToken(
	accessToken string,
) (*models.AccessTokenAuth, error) {
	authResult, err := sas.oauth2Provider.Authenticate(accessToken)
	if err != nil {
		return nil, err
	}
	sa, err := sas.repo.ServiceAccounts.ForEmail(authResult.Email)
	if _, ok := err.(*errors.EntityNotFoundError); ok {
		sa = &models.ServiceAccount{
			Name:    authResult.Email,
			Email:   authResult.Email,
			Picture: authResult.Picture,
		}
		if err = sas.Create(sa); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if authResult.Picture != "" && authResult.Picture != sa.Picture {
		sa.Picture = authResult.Picture
		if err = sas.repo.ServiceAccounts.Update(sa); err != nil {
			return nil, err
		}
	}
	return &models.AccessTokenAuth{
		ServiceAccountID: sa.ID,
		AccessToken:      authResult.AccessToken,
		Email:            authResult.Email,
	}, nil
}

// AuthenticateKeyPair verifies if key pair is valid
func (sas *serviceAccounts) AuthenticateKeyPair(
	keyID, keySecret string,
) (string, error) {
	sa, err := sas.repo.ServiceAccounts.ForKeyPair(keyID, keySecret)
	if err != nil {
		return "", err
	}
	return sa.ID, nil
}

// HasPermissionString checks if user has the ownership level required to take an
// action over a resource
func (sas serviceAccounts) HasPermissionString(
	serviceAccountID, permissionStr string,
) (bool, error) {
	ps, err := models.BuildPermission(permissionStr)
	if err != nil {
		return false, err
	}
	return sas.repo.ServiceAccounts.HasPermission(serviceAccountID, ps)
}

func (sas serviceAccounts) HasAllOwnerPermissions(
	serviceAccountID string, permissions []models.Permission,
) (bool, error) {
	psCpy := make([]models.Permission, len(permissions))
	for i := range permissions {
		psCpy[i] = permissions[i]
		psCpy[i].OwnershipLevel = models.OwnershipLevels.Owner
	}
	has, err := sas.HasPermissions(serviceAccountID, psCpy)
	if err != nil {
		return false, err
	}
	for i := range has {
		if !has[i] {
			return false, nil
		}
	}
	return true, nil
}

// HasPermissionsStrings returns an array of bools indicating whether a service
// account has some permissions
func (sas serviceAccounts) HasPermissionsStrings(
	serviceAccountID string, permissions []string,
) ([]bool, error) {
	pSl := make([]models.Permission, len(permissions))
	var err error
	for i := range permissions {
		pSl[i], err = models.BuildPermission(permissions[i])
		if err != nil {
			return nil, err
		}
	}
	return sas.HasPermissions(serviceAccountID, pSl)
}

// HasPermissions returns an array of bools indicating whether a service
// account has some permissions
func (sas serviceAccounts) HasPermissions(
	serviceAccountID string, permissions []models.Permission,
) ([]bool, error) {
	saPermissions, err := sas.GetPermissions(serviceAccountID)
	if err != nil {
		return nil, err
	}
	has := make([]bool, len(permissions))
	for i := range permissions {
		has[i] = permissions[i].IsPresent(saPermissions)
	}
	return has, nil
}

func serviceAccountHasPermissions(
	repo *repositories.All,
	serviceAccountID string,
	permissions []models.Permission,
) ([]bool, error) {
	saPermissions, err := serviceAccountGetPermissions(repo, serviceAccountID)
	if err != nil {
		return nil, err
	}
	has := make([]bool, len(permissions))
	for i := range permissions {
		has[i] = permissions[i].IsPresent(saPermissions)
	}
	return has, nil
}

func (sas serviceAccounts) GetPermissions(
	serviceAccountID string,
) ([]models.Permission, error) {
	return serviceAccountGetPermissions(sas.repo, serviceAccountID)
}

func serviceAccountGetPermissions(
	repo *repositories.All, serviceAccountID string,
) ([]models.Permission, error) {
	return repo.Permissions.ForServiceAccount(serviceAccountID)
}

func (sas serviceAccounts) CreatePermission(
	serviceAccountID string, permission *models.Permission,
) error {
	return createPermissionForServiceAccount(sas.repo, serviceAccountID, permission)
}

func createPermissionForServiceAccount(
	repo *repositories.All, serviceAccountID string, permission *models.Permission,
) error {
	sa, err := repo.ServiceAccounts.Get(serviceAccountID)
	if err != nil {
		return err
	}
	permission.RoleID = sa.BaseRoleID
	if err := repo.Permissions.Create(permission); err != nil {
		return err
	}
	return nil
}
