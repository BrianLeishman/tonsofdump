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
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "github.com/go-sql-driver/mysql"
	pb "gopkg.in/cheggaaa/pb.v1"
)

var db *sql.DB
var err error

var keysRegex = regexp.MustCompile(`(?s)(,\n\s+(?:KEY|FULLTEXT).+?),?\n\s*(?:\) ENGINE|CONSTRAINT)`)
var constraintsRegex = regexp.MustCompile(`(?s)(,\n\s+CONSTRAINT.+?),?\n\s*\) ENGINE`)

var createToAlterRegex = regexp.MustCompile(`\n\s+`)

type scehmaObject struct {
	defualtCharacterSet string
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

	flag.Parse()

	if StrEmpty(*usernamePtr) {
		panic("your MySQL username is required (-u)")
	}

	if StrEmpty(*passwordPtr) {
		panic("your MySQL password is required (-p)")
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

	tableNames := strings.Split(*tablesPtr, ",")
	tables := make([]tableObject, 0, len(tableNames))
	for _, t := range tableNames {
		tables = append(tables, tableObject{table: strings.TrimSpace(t)})
	}

	for i, t := range tables {
		log.Println("Getting create table for", t.table)
		tableCreateData := db.QueryRow("show create table`" + t.table + "`")
		err = tableCreateData.Scan(&tables[i].table, &tables[i].createTable)
		check(err)
	}

	if !StrEmpty(*directoryPtr) {
		directory = *directoryPtr
	} else {
		directory, err = ioutil.TempDir("", "tonsofdump-"+*databasePtr+"-"+*hostPtr+"-")
		check(err)
	}

	log.Println("Getting schema character set & collation")
	schemaData := db.QueryRow("select`DEFAULT_CHARACTER_SET_NAME`,`DEFAULT_COLLATION_NAME`" +
		"from`INFORMATION_SCHEMA`.`SCHEMATA`" +
		"where schema_name=database();")
	s := scehmaObject{}
	err = schemaData.Scan(&s.defualtCharacterSet, &s.defaultCollation)
	check(err)

	log.Println("Starting table loop")
	for i, t := range tables {
		log.Println("Starting table", t.table)
		log.Println("Getting primary key(s)")

		fileName := directory + "/" + t.table + ".sql"
		f, err := os.Create(fileName)
		check(err)

		w := bufio.NewWriter(f)

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

		log.Println("Done", primaryKeysString)

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

		w.WriteString("use`")
		w.WriteString(*databasePtr)
		w.WriteString("`;\n\n" +
			"SET FOREIGN_KEY_CHECKS=0;\n\n" +
			"SET global max_allowed_packet=1073741824;\nset @@wait_timeout=31536000;\n\n" +
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

		log.Println("Getting triggers...")
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
		log.Println("Done")

		log.Println("Getting average row size...")
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
		chunkSize := int(math.Round(8 * 1024 * 1024 / averageRowSize))
		if chunkSize < 1 {
			chunkSize = 1
		}
		log.Println("Done, avg row size:", averageRowSize, ", chunkSize:", chunkSize)

		log.Println("Getting row count...")
		countData := db.QueryRow("select count(*)from`" + t.table + "`")
		var count int
		err = countData.Scan(&count)
		check(err)
		log.Println("Done, count:", count)

		w.WriteString("drop table if exists`")
		w.WriteString(t.table)
		w.WriteString("`;\n\n")
		w.WriteString(tables[i].createTable)
		w.WriteString(";\n\n")

		log.Println("Getting columns...")
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
				c.characterSet = s.defualtCharacterSet
			}

			if StrEmpty(c.collation) {
				c.collation = s.defaultCollation
			}

			columns = append(columns, c)

			columnsCount++
		}
		log.Println("Done")

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

		log.Println("Getting table data...")
		start := time.Now()

		bar := pb.StartNew(count)

		for {
			data, err := db.Query("select"+columnsString+"from`"+t.table+"`where("+primaryKeysString+")>"+primaryValuesPlaceholder+"order by"+primaryKeysString+"limit "+strconv.Itoa(chunkSize), lastPrimaryValues...)
			check(err)

			j := 0
			for data.Next() {

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
						case "tinyint", "smallint", "mediumint", "int", "integer", "bigint":
							w.WriteString(strconv.Itoa(int(v[i].(int64))))
						case "float":
							w.WriteString(strconv.FormatFloat(float64(v[i].(float32)), 'E', -1, 32))
						case "double", "real":
							w.WriteString(strconv.FormatFloat(v[i].(float64), 'E', -1, 64))
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

				bar.Increment()
				j++
			}

			if j != 0 {
				w.WriteString(";\n")
			}

			if j < chunkSize {
				break
			}

		}

		w.WriteString("\n")

		bar.Finish()
		log.Println("Done, took", time.Since(start))

		log.Println("Getting create triggers...")
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
		log.Println("Done,", triggersCount)

		if hasKeyMatches {
			w.WriteString("alter table`")
			w.WriteString(t.table)
			w.WriteString("`")
			w.WriteString(createToAlterRegex.ReplaceAllString(strings.TrimLeft(keyMatches[1], ","), "\n  ADD "))
			w.WriteString(";\n\n")
		}

		log.Println("Getting foreign keys...")
		foreignKeysData, err := db.Query("select`CONSTRAINT_NAME`,`TABLE_NAME`,`COLUMN_NAME`,`REFERENCED_COLUMN_NAME`" +
			"from`information_schema`.`KEY_COLUMN_USAGE`" +
			"where`REFERENCED_TABLE_NAME`='" + t.table + "' " +
			"and`TABLE_SCHEMA`=database();")
		check(err)

		hasForeignKeys := false

		if hasConstraintMatches {
			w.WriteString("alter table`")
			w.WriteString(t.table)
			w.WriteString("`")
			w.WriteString(createToAlterRegex.ReplaceAllString(strings.TrimLeft(constraintMatches[1], ","), "\n  ADD "))
			w.WriteString(";\n\n")
		}

		for foreignKeysData.Next() {
			f := foreignKeyObject{}
			err = foreignKeysData.Scan(&f.constraint, &f.table, &f.column, &f.referencedColumn)
			check(err)

			w.WriteString("alter table`")
			w.WriteString(t.table)
			w.WriteString("`add constraint`")
			w.WriteString(f.column)
			w.WriteString("`foreign key(`")
			w.WriteString(f.constraint)
			w.WriteString("`)references`$Table`(`")
			w.WriteString(f.referencedColumn)
			w.WriteString("`);\n")

			hasForeignKeys = true
		}
		if hasForeignKeys {
			w.WriteString("\n")
		}
		log.Println("Done")

		w.WriteString("SET FOREIGN_KEY_CHECKS=1;\n\n")

		fmt.Println("Import dump file with `pv", fileName, "| mysql -u root -p -f`")

		w.Flush()
	}

	// fmt.Println("Dump files were stored in", directory)
}
