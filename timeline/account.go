/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package timeline

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Account represents an account on a data source. Some unexported
// fields require initialization from AddAccount() or LoadAccount().
type Account struct {
	ID int64 `json:"id"` // DB row ID
	// DataSourceID string `json:"data_source_id"`
	// User Person `json:"user"` // represents the account owner

	DataSource DataSource `json:"data_source"`

	authorization []byte
	t             *Timeline
}

func (acc *Account) fill(t *Timeline) error {
	ds, ok := dataSources[acc.DataSource.Name]
	if !ok {
		return fmt.Errorf("inconsistent DB: unrecognized data source ID: %s", acc.DataSource.Name)
	}
	acc.DataSource = ds

	acc.t = t

	return nil
}

// NewHTTPClient returns an HTTP client that is suitable for use
// with an API associated with the account's data source. If
// OAuth2 is configured for the data source, the client has OAuth2
// credentials. If a rate limit is configured, this client is
// rate limited. A sane default timeout is set, and any fields
// on the returned Client valule can be modified as needed.
func (acc Account) NewHTTPClient(ctx context.Context, oauth2 OAuth2, rl RateLimit) (*http.Client, error) {
	httpClient := new(http.Client)
	if oauth2.ProviderID != "" {
		var err error
		httpClient, err = acc.NewOAuth2HTTPClient(ctx, oauth2)
		if err != nil {
			return nil, err
		}
	}
	// TODO: rate limits will likely vary depending on whether user has their own Project/App with the service, or whether they're using ours... we should look into this
	if rl.RequestsPerHour > 0 {
		httpClient.Transport = acc.NewRateLimitedRoundTripper(httpClient.Transport, rl)
	}
	httpClient.Timeout = 60 * time.Second
	return httpClient, nil
}

// func (acc Account) String() string {
// 	return acc.DataSource.ID + "/" + acc.User.UserID
// }

// TODO: update godoc
// AddAccount adds a new account to the database. The account is with the
// given data source and owner. The account must not yet exist. This method
// does not attempt to authenticate with any API / hosted service.
func (t *Timeline) AddAccount(ctx context.Context, dataSourceID string, dsOptJSON json.RawMessage) (Account, error) {
	// ds, ok := dataSources[dataSourceID]
	// if !ok {
	// 	return Account{}, fmt.Errorf("data source not registered: %s", dataSourceID)
	// }

	// // datasource-specific options can be useful when interacting with it
	// dsOpt, err := ds.UnmarshalOptions(dsOptJSON)
	// if err != nil {
	// 	return Account{}, fmt.Errorf("unmarshaling data source options: %w", err)
	// }

	// // run input through the data source's account hook (if any)
	// if ds.NewAccount != nil {
	// 	owner, err = ds.NewAccount(owner, dsOpt)
	// 	if err != nil {
	// 		return Account{}, fmt.Errorf("data source account hook returned error: %w", err)
	// 	}
	// }

	// // add person if doesn't already exist
	// person, err := t.getOrMakePerson(ds.ID, owner)
	// if err != nil {
	// 	return Account{}, err
	// }

	// store the account
	var accountID int64
	t.dbMu.Lock()
	err := t.db.QueryRow(`INSERT INTO accounts (data_source_id) VALUES (?) RETURNING id`,
		dataSourceID).Scan(&accountID)
	t.dbMu.Unlock()
	if err != nil {
		return Account{}, fmt.Errorf("inserting into DB: %v", err)
	}

	// load the new account so caller can get its info (like ID)
	// TODO: should we just use context.Background() here? or pass in an actual context
	acct, err := t.LoadAccount(ctx, accountID)
	if err != nil {
		return Account{}, fmt.Errorf("loading new account: %v", err)
	}

	return acct, nil
}

func (acc *Account) AuthorizeOAuth2(ctx context.Context, oauth2 OAuth2) error {
	creds, err := authorizeWithOAuth2(ctx, oauth2)
	if err != nil {
		return err
	}
	return acc.SaveAuthorization(ctx, creds)
}

func (acc *Account) SaveAuthorization(ctx context.Context, credentials []byte) error {
	acc.t.dbMu.Lock()
	_, err := acc.t.db.ExecContext(ctx, `UPDATE accounts SET authorization=? WHERE id=?`, // TODO: LIMIT would be nice here
		credentials, acc.ID)
	acc.t.dbMu.Unlock()
	if err != nil {
		return fmt.Errorf("updating credentials in account row: %v", err)
	}
	return nil
}

// LoadAccount loads the account with the given ID from the database.
func (t *Timeline) LoadAccount(ctx context.Context, id int64) (Account, error) {
	var acc Account
	t.dbMu.RLock()
	err := t.db.QueryRowContext(ctx,
		`SELECT 
			accounts.id, accounts.authorization,
			data_sources.name
		FROM accounts, data_sources
		WHERE accounts.id=?
			AND data_sources.id = accounts.data_source_id
		LIMIT 1`,
		id).Scan(&acc.ID, &acc.authorization, &acc.DataSource.Name)
	t.dbMu.RUnlock()
	if err != nil {
		return acc, fmt.Errorf("querying account %d from DB: %v", id, err)
	}
	if err := acc.fill(t); err != nil {
		return acc, fmt.Errorf("filling account: %v", err)
	}
	return acc, nil
}

// LoadAccounts loads all the accounts with the given IDs and/or data source(s). If the
// slices are nil, all accounts will be loaded. If the slices are empty, no accounts will be.
func (t *Timeline) LoadAccounts(ids []int64, dataSourceIDs []string) ([]Account, error) {
	if (ids != nil && len(ids) == 0) ||
		(dataSourceIDs != nil && len(dataSourceIDs) == 0) {
		return []Account{}, nil
	}

	q := `
	SELECT
		accounts.id, accounts.authorization,
		data_sources.name
	FROM accounts, data_sources
	WHERE data_sources.id = accounts.data_source_id`
	var args []any
	if len(ids) > 0 || len(dataSourceIDs) > 0 {
		q += " AND ("
	}
	for i, id := range ids {
		if i > 0 {
			q += " OR "
		}
		q += "accounts.id=?"
		args = append(args, id)
	}
	if len(ids) > 0 && len(dataSourceIDs) > 0 {
		q += ") AND ("
	}
	for i, dsID := range dataSourceIDs {
		if i > 0 {
			q += " OR "
		}
		q += "data_sources.name=?"
		args = append(args, dsID)
	}
	if len(ids) > 0 || len(dataSourceIDs) > 0 {
		q += ")"
	}

	t.dbMu.RLock()
	defer t.dbMu.RUnlock()

	accounts := []Account{}
	rows, err := t.db.Query(q, args...)
	if err != nil {
		return accounts, fmt.Errorf("querying accounts from DB: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var acc Account
		err := rows.Scan(&acc.ID, &acc.authorization, &acc.DataSource.Name)
		if err != nil {
			return accounts, fmt.Errorf("scanning row: %v", err)
		}
		if err := acc.fill(t); err != nil {
			return accounts, err
		}
		accounts = append(accounts, acc)
	}
	if err = rows.Err(); err != nil {
		return accounts, fmt.Errorf("scanning account rows: %v", err)
	}

	return accounts, nil
}

// marshalGob is a convenient way to gob-encode v.
func marshalGob(v any) ([]byte, error) {
	b := new(bytes.Buffer)
	err := gob.NewEncoder(b).Encode(v)
	return b.Bytes(), err
}

// unmarshalGob is a convenient way to gob-decode data into v.
func unmarshalGob(data []byte, v any) error {
	return gob.NewDecoder(bytes.NewReader(data)).Decode(v)
}
