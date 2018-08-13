# tonsofdump
an ultra-fast MySQL backup tool written in Go

## Installation
You can download this like all other Go tools, if you don't know how to install or use Go programs, that's something you'll need to learn (it's easy though)

    go get github.com/BrianLeishman/tonsofdump

## Usage
### This will only work on tables that have primary keys!! It's very important to how this tool operates, and it **will not** function otherwise!

    tonsofdump -u root -p password -d myschema -h localhost -t 'mytable,mytable2,$procs'

This will create \*.sql files for each of those two tables listed and all the stored procedures on `myschema`

The pseudo tables are
- **$all** - Switches the dump tool to "all" mode, which downloads all tables (not views) *except* for the tables that are also listed. So `-t '$all,log'` will download every table on `myschema` *except* for `log`
- **$funcs** - Downloads all functions on `myschema` and stores them as "$funcs.sql"
- **$procs** - Downloads all stored procedures
- **$views** - Downloads all views

All string type fields get stored as hex literals for fast importing and absolutely no chance of SQL injection-style errors.

String type fields with character sets get stored with their collations inline, except for `BLOB` and `BINARY` types. For example, the value 'yeet' will get stored in the dump file like this (depending on your column character set/collation): `_utf8mb4 x'79656574'collate utf8mb4_unicode_ci`

Numeric fields, null values, and date/time fields get stored literally since they don't have the chance of containing characters that would break the MySQL queries.

------------

Why is this so fast? Well for starters, when the imported table is dropped and recreated, the recreation syntax doesn't include the indexes and foreign keys (except for the primary key and unique indexes). This way each insert isn't rebuilding each index at the time of insertion, and after every insert finishes the indexes are then added to the table.

Unique keys are included from the start in the event that somehow data exists that isn't unique (shouldn't be possible, but you never know), so that the unique keys aren't added at the end and can fail, which would be hard to debug.

Also the unique keys existing at the beginning allow us to run our insert statements as `insert ignore into...` and throw out the inconsistent duplicate values.

Insert queries (and the select statements used to download the data) try to hit an average command size of about 8MB, which is calculated by looking up the the average row size for the table in the information schema, and seeing how many rows would be needed to hit 8MB, which seems to be the sweet spot (at least in my testing) for getting the data downloaded as quickly as possible.

This also includes all triggers for a table when a table is downloaded, which the MySQL official dump tool seems to think are separate, but I think that's strange.

At the end of the dump tool running, there's a command outputted that can be used to import the saved \*.sql files, including an extra "$constraints.sql" file, which is a file that contains all the foreign key statements for *every* table, which is intended to be ran at the end of the import to account for possible circular foreign key references between tables.
