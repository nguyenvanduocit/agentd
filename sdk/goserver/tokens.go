package goserver

// TokenLookup finds paired daemons. An app implements it, and TokenKeeper, over its database (Tokens is both).
//
// Every method that takes a userID must answer only that user's daemons: a daemon of another user is ErrNotFound.
type TokenLookup interface {
	// ByHash finds the daemon whose Record.TokenHash is hash (hex SHA-256 of its token): the first thing a daemon's
	// handshake asks. ErrNotFound when no daemon has it; any other error means the store is down, and the daemon is told
	// to try again.
	ByHash(hash string) (Record, error)
	// ByID finds the user's daemon id. ErrNotFound when the user has no such daemon.
	ByID(userID, id string) (Record, error)
	// List is the user's daemons, the one paired first first; empty (not an error) when they have none.
	List(userID string) ([]Record, error)
}

// TokenKeeper changes the paired daemons. See TokenLookup for what every method owes the user condition.
type TokenKeeper interface {
	// Add keeps a new daemon for d.UserID and returns it as stored. If the user already has a daemon with d.Name, the
	// store gives the new one a free name or fails with ErrNameTaken.
	Add(d Record) (Record, error)
	// Rename gives the user's daemon id the name. ErrNotFound when the user has no such daemon, ErrNameTaken (possibly
	// wrapped) when another of their daemons has the name.
	Rename(userID, id, name string) (Record, error)
	// Delete removes the user's daemon id, so that its token stops working. ErrNotFound when the user has no such
	// daemon: a Delete that ignores the user would let one user disconnect another's daemon.
	Delete(userID, id string) error
	// Seen records when the daemon id was last connected or disconnected, in Unix milliseconds. It names no user: the
	// id comes from an authenticated socket.
	Seen(id string, at int64) error
}

// Tokens is where paired daemons are kept: TokenLookup and TokenKeeper. MemoryTokens is one in memory.
type Tokens interface {
	TokenLookup
	TokenKeeper
}
