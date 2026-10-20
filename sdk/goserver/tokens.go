package goserver

// TokenLookup finds paired daemons.
type TokenLookup interface {
	ByHash(hash string) (Record, error)
	ByID(userID, id string) (Record, error)
	List(userID string) ([]Record, error)
}

// TokenKeeper changes them.
type TokenKeeper interface {
	Add(d Record) (Record, error)
	Rename(userID, id, name string) (Record, error)
	Delete(userID, id string) error
	Seen(id string, at int64) error
}

// Tokens is where paired daemons are kept.
type Tokens interface {
	TokenLookup
	TokenKeeper
}
