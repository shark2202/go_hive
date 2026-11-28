package server

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// resolveColonyQueenForCreate returns the queen a create request should use. A
// create bound to an existing colony inherits that colony's recorded queen when
// the request names none and does not fork; otherwise the request's queen is
// kept. Malformed metadata is an error, a missing colony leaves queenName
// unchanged (the fresh-colony bootstrap owns that case).
func resolveColonyQueenForCreate(home hivehome.Home, colonyID, sourceSessionID, queenName *string) (*string, error) {
	// A fork (source_session_id present) names a new colony, so it keeps the
	// request's queen.
	if (sourceSessionID != nil && *sourceSessionID != "") ||
		(queenName != nil && strings.TrimSpace(*queenName) != "") ||
		colonyID == nil {
		return queenName, nil
	}
	resolved, err := colonyMetadataQueenName(home, *colonyID)
	if err != nil {
		return nil, err
	}
	if resolved != "" {
		return &resolved, nil
	}
	return queenName, nil
}
