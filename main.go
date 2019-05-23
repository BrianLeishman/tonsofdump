/*
 * File: c:\Users\Brian Leishman\go\src\github.com\BrianLeishman\tonsofdump\main.go
 * Project: c:\Users\Brian Leishman\go\src\github.com\BrianLeishman\tonsofdump
 * Created Date: Thursday July 5th 2018
 * Author: Brian Leishman
 * -----
 * Last Modified: Thu Jul 05 2018
 * Modified By: Brian Leishman
 * -----
 * Copyright (c) 2018 Stumpyinc, LLC
 */

package main

import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/fatih/color"
	_ "github.com/go-sql-driver/mysql"
	"github.com/vbauerster/mpb"
	"github.com/vbauerster/mpb/decor"
)

var db *sql.DB
var err error

var keysRegex = regexp.MustCompile(`(?s)(,\n\s+(?:KEY|FULLTEXT).+?),?\n\s*(?:\) ENGINE|CONSTRAINT)`)
var constraintsRegex = regexp.MustCompile(`(?s)(,\n\s+CONSTRAINT.+?),?\n\s*\) ENGINE`)

var createToAlterRegex = regexp.MustCompile(`\n\s+`)
var constraintsCreateToAlterRegex = regexp.MustCompile(`,?\n\s+`)

type schemaObject struct {
	defaultCharacterSet string
	defaultCollation    string
}

type tableObject struct {
	table       string
	createTable string
	primaryKeys []string
}

type columnObject struct {
	column       string
	characterSet string
	collation    string
	dataType     string
}

type foreignKeyObject struct {
	constraint       string
	table            string
	column           string
	referencedColumn string
}

// StrEmpty checks whether string contains only whitespace or not
func StrEmpty(s string) bool {
	if len(s) == 0 {
		return true
	}

	r := []rune(s)
	l := len(r)

	for l > 0 {
		l--
		if !unicode.IsSpace(r[l]) {
			return false
		}
	}

	return true
}

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func main() {

	usernamePtr := flag.String("u", "root", "your MySQL username")
	passwordPtr := flag.String("p", "", "your MySQL password")
	hostPtr := flag.String("h", "localhost", "your MySQL host")
	portPtr := flag.Int("P", 3306, "your MySQL port")
	databasePtr := flag.String("d", "", "your MySQL database")

	tablesPtr := flag.String("t", "", "comma separated list of tables to dump")

	directoryPtr := flag.String("f", "", "the storage root for companies of downloaded files")

	bytesPtr := flag.Int("b", 8*1024*1024, "the chunk size in bytes (defaults to 8388608, or 8MB)")
	maxTablesPtr := flag.Int("m", runtime.NumCPU(), "number of max threads/tables to download at once")

	zipPtr := flag.Bool("z", false, "set to compress to a *.tar.bz2 file (requires that tar and lbzip2 are installed)")
	noDataPtr := flag.Bool("n", false, "skips downloading data for tables")

	flag.Parse()

	if StrEmpty(*usernamePtr) {
		panic("your MySQL username is required (-u)")
	}

	if StrEmpty(*hostPtr) {
		panic("your MySQL host is required (-h)")
	}

	if StrEmpty(*databasePtr) {
		panic("your MySQL database is required (-d)")
	}

	if StrEmpty(*tablesPtr) {
		panic("you need to give at least one table (-t)")
	}

	var directory string

	db, err = sql.Open("mysql", *usernamePtr+":"+*passwordPtr+"@tcp("+*hostPtr+":"+strconv.Itoa(*portPtr)+")/"+*databasePtr+"?charset=utf8mb4&collation=utf8mb4_unicode_ci")
	check(err)

	err = db.Ping()
	check(err)

	all := false
	procs := false
	funcs := false
	views := false

	var wg sync.WaitGroup
	pb := mpb.New(mpb.WithWaitGroup(&wg))
	tablesCh := make(chan struct{}, *maxTablesPtr)
	foreignKeysMutex := &sync.Mutex{}

	tableNames := strings.Split(*tablesPtr, ",")
	tables := make([]tableObject, 0, len(tableNames))
	for _, t := range tableNames {
		table := strings.TrimSpace(t)
		switch strings.ToLower(table) {
		case "$all":
			all = true
		case "$procs":
			procs = true
		case "$funcs", "$functions":
			funcs = true
		case "$views":
			views = true
		default:
			tables = append(tables, tableObject{table: table})
		}
	}

	if all {
		tablesKeys := make(map[string]struct{}, len(tables))
		for _, t := range tables {
			tablesKeys[t.table] = struct{}{}
		}

		tablesData, err := db.Query("select`TABLE_NAME`" +
			"from`information_schema`.`tables`" +
			"where`table_schema`=database()" +
			"and`TABLE_COMMENT`<>'VIEW';")
		check(err)

		tables = make([]tableObject, 0, len(tableNames))

		for tablesData.Next() {
			var table string
			err = tablesData.Scan(&table)
			check(err)

			if _, ok := tablesKeys[table]; !ok {
				tables = append(tables, tableObject{table: table})
			}
		}
	}

	prefix := "tonsofdump-" + *databasePtr + "-" + *hostPtr + "-" + time.Now().Format("20060102150405")
	if !StrEmpty(*directoryPtr) {
		directory = *directoryPtr + "/" + prefix
	} else {
		directory, err = ioutil.TempDir("", prefix)
		check(err)
	}

	os.MkdirAll(directory, os.ModePerm)

	if procs {
		wg.Add(1)
		tablesCh <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-tablesCh }()

			log.Println("Getting procs...")
			procsCount := 0

			f, err := os.Create(directory + "/$procs.sql")
			check(err)
			defer f.Sync()
			defer f.Close()

			w := bufio.NewWriter(f)

			procsData, err := db.Query("show procedure status where`Db`=database()")
			check(err)

			w.WriteString("use`")
			w.WriteString(*databasePtr)
			w.WriteString("`;\n\n")

			for procsData.Next() {
				var proc string
				var x interface{}
				err = procsData.Scan(&x, &proc, &x, &x, &x, &x, &x, &x, &x, &x, &x)
				check(err)

				procData := db.QueryRow("show create procedure`" + proc + "`")
				var createProc string
				err = procData.Scan(&x, &x, &createProc, &x, &x, &x)
				check(err)

				w.WriteString("drop procedure if exists`")
				w.WriteString(proc)
				w.WriteString("`;\n\nDELIMITER $$\n")
				w.WriteString(createProc)
				w.WriteString("$$\nDELIMITER ;\n\n")

				procsCount++
			}

			w.Flush()
		}()
	}

	if funcs {
		wg.Add(1)
		tablesCh <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-tablesCh }()

			log.Println("Getting funcs...")
			funcsCount := 0

			f, err := os.Create(directory + "/$funcs.sql")
			check(err)
			defer f.Sync()
			defer f.Close()

			w := bufio.NewWriter(f)

			funcsData, err := db.Query("show function status where`Db`=database()")
			check(err)

			w.WriteString("use`")
			w.WriteString(*databasePtr)
			w.WriteString("`;\n\n")

			for funcsData.Next() {
				var function string
				var x interface{}
				err = funcsData.Scan(&x, &function, &x, &x, &x, &x, &x, &x, &x, &x, &x)
				check(err)

				funcData := db.QueryRow("show create function`" + function + "`")
				var createFunc string
				err = funcData.Scan(&x, &x, &createFunc, &x, &x, &x)
				check(err)

				w.WriteString("drop function if exists`")
				w.WriteString(function)
				w.WriteString("`;\n\nDELIMITER $$\n")
				w.WriteString(createFunc)
				w.WriteString("$$\nDELIMITER ;\n\n")

				funcsCount++
			}

			w.Flush()
		}()
	}

	if views {
		wg.Add(1)
		tablesCh <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-tablesCh }()

			log.Println("Getting views...")
			viewsCount := 0

			f, err := os.Create(directory + "/$views.sql")
			check(err)
			defer f.Sync()
			defer f.Close()

			w := bufio.NewWriter(f)

			viewsData, err := db.Query("select`TABLE_NAME`" +
				"from`information_schema`.`tables`" +
				"where`table_schema`=database()" +
				"and`TABLE_COMMENT`='VIEW';")
			check(err)

			w.WriteString("use`")
			w.WriteString(*databasePtr)
			w.WriteString("`;\n\n")

			for viewsData.Next() {
				var view string
				err = viewsData.Scan(&view)
				check(err)

				viewData := db.QueryRow("show create view`" + view + "`")
				var createView string
				var x interface{}
				err = viewData.Scan(&x, &createView, &x, &x)
				check(err)

				w.WriteString("drop view if exists`")
				w.WriteString(view)
				w.WriteString("`;\n\n")
				w.WriteString(createView)
				w.WriteString(";\n\n")

				viewsCount++
			}

			w.Flush()
		}()
	}

	for i, t := range tables {
		tableCreateData := db.QueryRow("show create table`" + t.table + "`")
		err = tableCreateData.Scan(&tables[i].table, &tables[i].createTable)
		check(err)
	}

	schemaData := db.QueryRow("select`DEFAULT_CHARACTER_SET_NAME`,`DEFAULT_COLLATION_NAME`" +
		"from`INFORMATION_SCHEMA`.`SCHEMATA`" +
		"where schema_name=database();")
	s := schemaObject{}
	err = schemaData.Scan(&s.defaultCharacterSet, &s.defaultCollation)
	check(err)

	constraintsFileName := directory + "/$constraints.sql"
	constraintsFile, err := os.Create(constraintsFileName)
	check(err)

	defer constraintsFile.Sync()
	defer constraintsFile.Close()

	constraintsWriter := bufio.NewWriter(constraintsFile)

	constraintsWriter.WriteString("use`")
	constraintsWriter.WriteString(*databasePtr)
	constraintsWriter.WriteString("`;\n\nset foreign_key_checks=0;\nset unique_checks=0;\n\n")

	for i, t := range tables {
		wg.Add(1)
		tablesCh <- struct{}{}
		go func(i int, t tableObject) {
			defer wg.Done()
			defer func() { <-tablesCh }()

			fileName := directory + "/" + t.table + ".sql"
			f, err := os.Create(fileName)
			check(err)

			writers := []*bufio.Writer{constraintsWriter, nil}
			w := bufio.NewWriter(f)

			writers[1] = w

			defer f.Sync()
			defer f.Close()

			keysData, err := db.Query("select`COLUMN_NAME`" +
				"from`INFORMATION_SCHEMA`.`STATISTICS`" +
				"where table_name='" + t.table + "'" +
				"and`INDEX_NAME`='PRIMARY'" +
				"and`INDEX_SCHEMA`=database()")
			check(err)

			primaryKeysString := ""
			primaryKeysCount := 0
			primaryValuesPlaceholder := "("
			first := true

			for keysData.Next() {
				var columnName string
				err = keysData.Scan(&columnName)
				check(err)

				tables[i].primaryKeys = append(tables[i].primaryKeys, columnName)

				if !first {
					primaryKeysString += ","
					primaryValuesPlaceholder += ","
				} else {
					first = false
				}

				primaryKeysString += "`" + columnName + "`"
				primaryValuesPlaceholder += "?"

				primaryKeysCount++
			}

			if first {
				panic("No primary keys were found on the table")
			}

			primaryValuesPlaceholder += ")"

			keyMatches := keysRegex.FindStringSubmatch(t.createTable)
			hasKeyMatches := len(keyMatches) >= 2
			if hasKeyMatches {
				tables[i].createTable = strings.Replace(tables[i].createTable, keyMatches[1], "", 1)
			}

			constraintMatches := constraintsRegex.FindStringSubmatch(t.createTable)
			hasConstraintMatches := len(constraintMatches) >= 2
			if hasConstraintMatches {
				tables[i].createTable = strings.Replace(tables[i].createTable, constraintMatches[1], "", 1)
			}

			w.WriteString("\\! echo \"Starting ")
			w.WriteString(t.table)
			w.WriteString(" inserts\";\n")

			w.WriteString("use`")
			w.WriteString(*databasePtr)
			w.WriteString("`;\n\nset foreign_key_checks=0;\nset unique_checks=0;\nset autocommit=0;\n\n")

			w.WriteString("SET global max_allowed_packet=1073741824;\nset @@wait_timeout=31536000;\n\n" +
				"DROP procedure IF EXISTS `_tonsofdatabase_remove_constraints`;\n" +
				"DELIMITER $$\n" +
				"USE `sterling`$$\n" +
				"CREATE DEFINER=`root`@`%` PROCEDURE `_tonsofdatabase_remove_constraints`(in `$Table` text, in `$Database` text)\n" +
				"begin\n" +
				"	declare done int default FALSE;\n" +
				"	declare dropCommand text;\n" +
				"	declare dropCur cursor for\n" +
				"		select concat('alter table`',`TABLE_NAME`,'`DROP FOREIGN KEY`',`CONSTRAINT_NAME`, '`;')\n" +
				"		from `information_schema`.`KEY_COLUMN_USAGE`\n" +
				"		where `REFERENCED_TABLE_NAME` = `$Table`\n" +
				"		and `TABLE_SCHEMA` = `$Database`;\n" +
				"	declare continue handler for not found set done = true;\n" +
				"	open dropCur;\n" +
				"	read_loop: loop\n" +
				"	fetch dropCur into dropCommand;\n" +
				"	if done then\n" +
				"		leave read_loop;\n" +
				"	end if;\n" +
				"	set @sdropCommand = dropCommand;\n" +
				"	prepare dropClientUpdateKeyStmt from @sdropCommand;\n" +
				"	execute dropClientUpdateKeyStmt;\n" +
				"		deallocate prepare dropClientUpdateKeyStmt;\n" +
				"	end loop;\n" +
				"	close dropCur;\n" +
				"end$$\n" +
				"DELIMITER ;\n" +
				"call`_tonsofdatabase_remove_constraints`('")
			w.WriteString(t.table)
			w.WriteString("','")
			w.WriteString(*databasePtr)
			w.WriteString("');\n\n")

			triggersData, err := db.Query("select`TRIGGER_NAME`" +
				"from`information_schema`.`triggers`" +
				"where`EVENT_OBJECT_TABLE`='" + t.table + "' " +
				"and`EVENT_OBJECT_SCHEMA`=database()")
			check(err)
			triggers := make([]string, 0)
			for triggersData.Next() {
				var triggerName string
				err = triggersData.Scan(&triggerName)
				check(err)

				w.WriteString("drop trigger if exists `")
				w.WriteString(triggerName)
				w.WriteString("`;\n\n")

				triggers = append(triggers, triggerName)
			}

			averageRowSizeData := db.QueryRow("select avg_row_length " +
				"from`information_schema`.tables " +
				"where table_name='" + t.table + "'" +
				"and`TABLE_SCHEMA`=database();")
			var averageRowSize float64
			err = averageRowSizeData.Scan(&averageRowSize)
			check(err)
			if averageRowSize <= 0 {
				averageRowSize = 1024
			}
			chunkSize := int(math.Round(float64(*bytesPtr) / averageRowSize))
			if chunkSize < 1 {
				chunkSize = 1
			}

			countData := db.QueryRow("select count(*)from`" + t.table + "`")
			var count int
			err = countData.Scan(&count)
			check(err)

			w.WriteString("drop table if exists`")
			w.WriteString(t.table)
			w.WriteString("`;\n\n")
			w.WriteString(tables[i].createTable)
			w.WriteString(";\n\n")

			columnsData, err := db.Query("select`COLUMN_NAME`,ifnull(`CHARACTER_SET_NAME`,''),ifnull(`COLLATION_NAME`,''),`DATA_TYPE`" +
				"from`INFORMATION_SCHEMA`.`COLUMNS`" +
				"where`TABLE_NAME`='" + t.table + "' " +
				"and`TABLE_SCHEMA`=database()" +
				"and`EXTRA`not in('VIRTUAL GENERATED','STORED GENERATED')" +
				"order by`ordinal_position`;")
			check(err)
			columnsString := ""
			columns := []columnObject{}
			columnsCount := 0
			columnsKeys := make(map[string]int)
			for columnsData.Next() {
				c := columnObject{}
				err = columnsData.Scan(&c.column, &c.characterSet, &c.collation, &c.dataType)
				check(err)

				columnsKeys[c.column] = columnsCount

				if columnsCount != 0 {
					columnsString += ","
				}

				columnsString += "`" + c.column + "`"

				if StrEmpty(c.characterSet) {
					c.characterSet = s.defaultCharacterSet
				}

				if StrEmpty(c.collation) {
					c.collation = s.defaultCollation
				}

				columns = append(columns, c)

				columnsCount++
			}

			lastPrimaryValues := make([]interface{}, primaryKeysCount, primaryKeysCount)
			for i := 0; i < primaryKeysCount; i++ {
				lastPrimaryValues[i] = ""
			}

			v := make([]interface{}, columnsCount, columnsCount)
			p := make([]interface{}, columnsCount, columnsCount)
			for i := 0; i < columnsCount; i++ {
				p[i] = &v[i]
			}

			t = tables[i]

			if !*noDataPtr {
				var bar *mpb.Bar
				if count != 0 {
					bar = pb.AddBar(int64(count),
						mpb.PrependDecorators(
							// simple name decorator
							decor.Name(fmt.Sprintf("%s:", t.table)),
							// decor.DSyncWidth bit enables column width synchronization
							decor.Percentage(decor.WCSyncSpace),
						),
						mpb.BarRemoveOnComplete(),
					)
				}
				k := 0
				for {
					var data *sql.Rows
					if k == 0 {
						data, err = db.Query("select" + columnsString + "from`" + t.table + "`order by" + primaryKeysString + "limit " + strconv.Itoa(chunkSize))
					} else {
						data, err = db.Query("select"+columnsString+"from`"+t.table+"`where("+primaryKeysString+")>"+primaryValuesPlaceholder+"order by"+primaryKeysString+"limit "+strconv.Itoa(chunkSize), lastPrimaryValues...)
					}
					check(err)

					j := 0
					for data.Next() {
						start := time.Now()
						if j != 0 {
							w.WriteString(",")
						} else {
							w.WriteString("insert ignore into`")
							w.WriteString(t.table)
							w.WriteString("`(")
							w.WriteString(columnsString)
							w.WriteString(")values")
						}

						w.WriteString("(")

						err = data.Scan(p...)
						check(err)

						for i := 0; i < columnsCount; i++ {
							if i != 0 {
								w.WriteString(",")
							}
							if v[i] == nil {
								w.WriteString("null")
							} else {
								switch columns[i].dataType {
								case "decimal", "numeric":
									w.Write(v[i].([]byte))
								case "date", "timestamp":
									w.WriteString("'")
									w.Write(v[i].([]byte))
									w.WriteString("'")
								case "tinyint", "int", "smallint", "mediumint", "integer", "bigint":
									switch n := v[i].(type) {
									case int64:
										w.WriteString(strconv.Itoa(int(v[i].(int64))))
									case []uint8:
										w.WriteString(string(v[i].([]uint8)))
									default:
										log.Fatalln("type not handled", v[i], n)
									}
								case "float":
									switch n := v[i].(type) {
									case float32:
										w.WriteString(strconv.FormatFloat(float64(v[i].(float32)), 'E', -1, 32))
									case []uint8:
										w.WriteString(string(v[i].([]uint8)))
									default:
										log.Fatalln("type not handled", v[i], n)
									}
								case "double", "real":
									switch n := v[i].(type) {
									case float32:
										w.WriteString(strconv.FormatFloat(v[i].(float64), 'E', -1, 64))
									case []uint8:
										w.WriteString(string(v[i].([]uint8)))
									default:
										log.Fatalln("type not handled", v[i], n)
									}
								case "binary", "bit", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
									fmt.Fprintf(w, "x'%x'", v[i])
								default:
									w.WriteString("_")
									w.WriteString(columns[i].characterSet)
									fmt.Fprintf(w, " x'%x'collate ", v[i])
									w.WriteString(columns[i].collation)
								}
							}
						}

						w.WriteString(")")

						for i := 0; i < primaryKeysCount; i++ {
							lastPrimaryValues[i] = v[columnsKeys[t.primaryKeys[i]]]
						}

						bar.IncrBy(1, time.Since(start))
						j++
					}

					if j != 0 {
						w.WriteString(";\n")
						w.WriteString("\\! echo \"")
						w.WriteString(t.table)
						w.WriteString(" ")
						w.WriteString(strconv.Itoa(int(float64(bar.Current()) / float64(count) * float64(100))))
						w.WriteString("%\";\n")
					}

					if j < chunkSize {
						break
					}

					k++
				}

				w.WriteString("\n")
			}

			triggersCount := 0
			for _, t := range triggers {
				triggerData := db.QueryRow("SHOW CREATE trigger`" + t + "`;")
				var createTrigger string
				var x interface{}
				err = triggerData.Scan(&x, &x, &createTrigger, &x, &x, &x, &x)
				check(err)

				w.WriteString("DELIMITER $$\n")
				w.WriteString(createTrigger)
				w.WriteString("$$\nDELIMITER ;\n\n")

				triggersCount++
			}

			w.WriteString("\\! echo \"Creating keys and indexes for ")
			w.WriteString(t.table)
			w.WriteString("\";\n")

			if hasKeyMatches {
				w.WriteString("alter table`")
				w.WriteString(t.table)
				w.WriteString("`")
				w.WriteString(createToAlterRegex.ReplaceAllString(strings.TrimLeft(keyMatches[1], ","), "\n  ADD "))
				w.WriteString(";\n\n")
			}

			foreignKeysData, err := db.Query("select`CONSTRAINT_NAME`,`TABLE_NAME`,`COLUMN_NAME`,`REFERENCED_COLUMN_NAME`" +
				"from`information_schema`.`KEY_COLUMN_USAGE`" +
				"where`REFERENCED_TABLE_NAME`='" + t.table + "' " +
				"and`TABLE_SCHEMA`=database();")
			check(err)

			hasForeignKeys := false
			constraintsReplacement := ";\nalter table`" + t.table + "`add "

			for i, w := range writers {
				// Lock if if the writer being used is the foreign key writer
				// so the different table threads don't try to write to it at the
				// same time
				if i == 0 {
					foreignKeysMutex.Lock()
				}

				// If the table has foreign keys as part of the table syntax,
				// we need to add that back now
				if hasConstraintMatches {
					w.WriteString(constraintsCreateToAlterRegex.ReplaceAllString(strings.TrimLeft(constraintMatches[1], ","), constraintsReplacement))
					w.WriteString(";\n\n")
				}

				// And we also need to recreate each foreign that references this table
				for foreignKeysData.Next() {
					f := foreignKeyObject{}
					err = foreignKeysData.Scan(&f.constraint, &f.table, &f.column, &f.referencedColumn)
					check(err)

					w.WriteString("alter table`")
					w.WriteString(f.table)
					w.WriteString("`add constraint`")
					w.WriteString(f.constraint)
					w.WriteString("`foreign key(`")
					w.WriteString(f.column)
					w.WriteString("`)references`")
					w.WriteString(t.table)
					w.WriteString("`(`")
					w.WriteString(f.referencedColumn)
					w.WriteString("`);\n")

					hasForeignKeys = true
				}
				if hasForeignKeys {
					w.WriteString("\n")
				}

				// Unlock the foreign keys writer to let another thread access that writer
				if i == 0 {
					w.Flush()
					foreignKeysMutex.Unlock()
				}
			}

			// Turn back on foreign key checking and unique key checks
			// and commit the insert statements after all the inserts have been executed
			w.WriteString("commit;\nset foreign_key_checks=1;\nset unique_checks=1;\n\n")

			// Flush the writer (if the program exits without flushing
			// then they never get written to the file)
			w.Flush()
		}(i, t)
	}

	pb.Wait()

	constraintsWriter.WriteString("set foreign_key_checks=1;\nset unique_checks=1;\nset autocommit=1;\n\n")
	constraintsWriter.Flush()

	if *zipPtr {
		err = exec.Command("tar", "-cf", directory+".tar.bz2", "--remove-files", "-C", directory, ".", "--use-compress-program=lbzip2").Run()
		if err != nil {
			log.Fatalln(err)
		}
	} else {
		importCommand := "cd '" + directory + "' && pv"
		for _, t := range tables {
			importCommand += " '" + t.table + ".sql'"
		}

		if funcs {
			importCommand += " '$funcs.sql'"
		}

		if procs {
			importCommand += " '$procs.sql'"
		}

		if views {
			importCommand += " '$views.sql'"
		}

		importCommand += " '$constraints.sql' | mysql -fc -u root -p"

		color.Cyan("%s\n", importCommand)
	}

}
