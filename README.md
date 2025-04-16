# matrixbackup #

This is a tool that implements downloading of data from all rooms user
has access to, to a set of json files. The json files are stored in
hierarchy room-human-readable-alias:room-id/yyyy-mm-dd/data.json,
where data.json contains all data for that particular day for that
room.

The default credentials file is
`~/.config/matrix-commander/credentials.json`.  Command-line flags
always override values found in the config file.

## Usage example ##

```
go run github.com/fingon/go-matrixbackup -server <YOUR_HOMESERVER_URL> -user <YOUR_USER_ID> -token <YOUR_ACCESS_TOKEN> -dir ./my_matrix_backup
```

or (if you are Matrix Commander user, you have your credentials in
~/.config/matrix-commander/credentials.json, and you want backups in
backup/ directory)

```
go run github.com/fingon/go-matrixbackup
```
