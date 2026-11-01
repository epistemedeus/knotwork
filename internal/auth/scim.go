package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"knotwork/internal/domain"
)

const (
	scimUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

type scimEmail struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

type scimName struct {
	Formatted string `json:"formatted,omitempty"`
}

type scimUser struct {
	Schemas    []string    `json:"schemas"`
	ID         string      `json:"id"`
	ExternalID string      `json:"externalId,omitempty"`
	UserName   string      `json:"userName"`
	Name       scimName    `json:"name,omitempty"`
	Emails     []scimEmail `json:"emails,omitempty"`
	Active     bool        `json:"active"`
}

type scimList struct {
	Schemas      []string   `json:"schemas"`
	TotalResults int        `json:"totalResults"`
	Resources    []scimUser `json:"Resources"`
}

func (s *Service) RegisterSCIM(mux *http.ServeMux) {
	mux.Handle("GET /scim/v2/Users", s.RequireSCIM(http.HandlerFunc(s.scimListUsers)))
	mux.Handle("POST /scim/v2/Users", s.RequireSCIM(http.HandlerFunc(s.scimCreateUser)))
	mux.Handle("GET /scim/v2/Users/{id}", s.RequireSCIM(http.HandlerFunc(s.scimGetUser)))
	mux.Handle("PATCH /scim/v2/Users/{id}", s.RequireSCIM(http.HandlerFunc(s.scimPatchUser)))
	mux.Handle("DELETE /scim/v2/Users/{id}", s.RequireSCIM(http.HandlerFunc(s.scimDeleteUser)))
	mux.Handle("GET /scim/v2/Groups", s.RequireSCIM(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSCIM(w, http.StatusOK, scimList{Schemas: []string{scimListSchema}, Resources: []scimUser{}})
	})))
	mux.Handle("POST /scim/v2/Groups", s.RequireSCIM(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSCIMError(w, http.StatusNotImplemented, "SCIM groups are not stored by Knotwork; membership is managed by OIDC")
	})))
}

func (s *Service) scimListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.DB.ListUsers(r.Context())
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	resources := make([]scimUser, 0, len(users))
	for _, u := range users {
		if filter != "" && !matchesSCIMFilter(u, filter) {
			continue
		}
		resources = append(resources, toSCIM(u))
	}
	writeSCIM(w, http.StatusOK, scimList{Schemas: []string{scimListSchema}, TotalResults: len(resources), Resources: resources})
}

func (s *Service) scimCreateUser(w http.ResponseWriter, r *http.Request) {
	var in scimUser
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if in.UserName == "" {
		writeSCIMError(w, http.StatusBadRequest, "userName is required")
		return
	}
	externalID := in.ExternalID
	if externalID == "" {
		externalID = in.UserName
	}
	if existing, err := s.DB.GetUserBySCIMID(r.Context(), externalID); err == nil {
		writeSCIM(w, http.StatusConflict, toSCIM(existing))
		return
	}
	email := ""
	for _, e := range in.Emails {
		if e.Primary || email == "" {
			email = e.Value
		}
	}
	display := in.Name.Formatted
	if display == "" {
		display = in.UserName
	}
	user, err := s.DB.CreateSCIMUser(r.Context(), externalID, email, in.UserName, display)
	if err != nil {
		writeSCIMError(w, http.StatusConflict, "userName, email, or externalId already exists")
		return
	}
	writeSCIM(w, http.StatusCreated, toSCIM(user))
}

func (s *Service) scimGetUser(w http.ResponseWriter, r *http.Request) {
	user, err := s.lookupSCIMUser(r)
	if err != nil {
		writeSCIMError(w, http.StatusNotFound, "user not found")
		return
	}
	writeSCIM(w, http.StatusOK, toSCIM(user))
}

type scimPatch struct {
	Schemas    []string `json:"schemas"`
	Operations []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"Operations"`
}

func (s *Service) scimPatchUser(w http.ResponseWriter, r *http.Request) {
	user, err := s.lookupSCIMUser(r)
	if err != nil {
		writeSCIMError(w, http.StatusNotFound, "user not found")
		return
	}
	var patch scimPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	active := user.IsActive
	for _, op := range patch.Operations {
		if !strings.EqualFold(op.Op, "replace") && !strings.EqualFold(op.Op, "add") {
			continue
		}
		if op.Path == "active" || op.Path == "" {
			var b bool
			if json.Unmarshal(op.Value, &b) == nil {
				active = b
				continue
			}
			var obj map[string]any
			if json.Unmarshal(op.Value, &obj) == nil {
				if v, ok := obj["active"].(bool); ok {
					active = v
				}
			}
		}
	}
	if err := s.DB.SetUserActive(r.Context(), user.ID, active); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "could not update user")
		return
	}
	if !active {
		s.Log.Warn("scim deactivated user", "user", user.Username)
	}
	user.IsActive = active
	writeSCIM(w, http.StatusOK, toSCIM(user))
}

func (s *Service) scimDeleteUser(w http.ResponseWriter, r *http.Request) {
	user, err := s.lookupSCIMUser(r)
	if err != nil {
		writeSCIMError(w, http.StatusNotFound, "user not found")
		return
	}
	if err := s.DB.SetUserActive(r.Context(), user.ID, false); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "could not deactivate user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) lookupSCIMUser(r *http.Request) (domain.User, error) {
	id := r.PathValue("id")
	user, err := s.DB.GetUserByID(r.Context(), id)
	if err == nil {
		return user, nil
	}
	user, err = s.DB.GetUserBySCIMID(r.Context(), id)
	if err != nil {
		return domain.User{}, errors.New("user not found")
	}
	return user, nil
}

func matchesSCIMFilter(u domain.User, filter string) bool {
	fields := strings.Fields(filter)
	if len(fields) != 3 || !strings.EqualFold(fields[1], "eq") {
		return true
	}
	want := strings.Trim(fields[2], `"`)
	switch strings.ToLower(fields[0]) {
	case "username":
		return strings.EqualFold(u.Username, want)
	case "externalid":
		return u.ExternalID == want
	case "emails.value":
		return strings.EqualFold(u.Email, want)
	}
	return true
}

func toSCIM(u domain.User) scimUser {
	out := scimUser{
		Schemas:    []string{scimUserSchema},
		ID:         u.ID,
		ExternalID: u.SCIMID,
		UserName:   u.Username,
		Name:       scimName{Formatted: u.DisplayName},
		Active:     u.IsActive,
	}
	if u.Email != "" {
		out.Emails = []scimEmail{{Value: u.Email, Primary: true, Type: "work"}}
	}
	return out
}

func writeSCIM(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeSCIMError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schemas": []string{scimErrorSchema},
		"detail":  detail,
		"status":  status,
	})
}
