package handlers

import (
	"errors"
	"net/http"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/store"
)

// sqlStater is implemented by a database driver's error (pgconn.PgError has SQLState()). The control
// plane does not import a driver, so it recognises a constraint violation through this method.
type sqlStater interface{ SQLState() string }

// SQLSTATE codes a create can run into.
const (
	sqlStateUniqueViolation = "23505"
	sqlStateForeignKey      = "23503"
)

// respondCreateError answers a store error from a create or update. A missing thing is 404, a
// duplicate (the store's *ErrConflict, or a unique-constraint violation that reached us raw from a
// store built outside this repository) is 409, and anything else is a 500 that says what failed without
// the driver's text: a database error carries SQL and constraint names the caller must not see.
func respondCreateError(w http.ResponseWriter, err error, what string) {
	var nf *store.ErrNotFound
	var cf *store.ErrConflict
	var st sqlStater
	switch {
	case errors.As(err, &nf):
		respondError(w, http.StatusNotFound, nf.Error())
	case errors.As(err, &cf):
		respondError(w, http.StatusConflict, cf.Error())
	case errors.As(err, &st) && st.SQLState() == sqlStateUniqueViolation:
		respondError(w, http.StatusConflict, what+" already exists")
	case errors.As(err, &st) && st.SQLState() == sqlStateForeignKey:
		respondError(w, http.StatusConflict, what+" refers to something that does not exist or is still in use")
	default:
		log.Error().Err(err).Str("what", what).Msg("store error")
		respondError(w, http.StatusInternalServerError, "could not "+what)
	}
}
