package main

// Executes database migrations to the latest target version. In production this
// requires the root password for MySQL. The user will be prompted for that so
// it is not entered via the command line.

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/golang/glog"
	"skia.googlesource.com/buildbot.git/perf/go/db"
)

// flags
var (
	dbConnString = flag.String("db_conn_string", "root:%s@tcp(173.194.104.24:3306)/skia?parseTime=true", "\n\tDatabase string to open connect to the MySQL database. "+
		"\n\tNeeds to follow the format of the golang-mysql driver (https://github.com/go-sql-driver/mysql."+
		"\n\tIf the string contains %s the user will be prompted to enter a password which will then be used for subtitution.")
)

func main() {
	flag.Parse()

	var connectionStr = *dbConnString

	// if it contains formatting information read the password from stdin.
	if strings.Contains(connectionStr, "%s") {
		glog.Infof("Using connection string: %s", connectionStr)
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Enter password for MySQL: ")
		password, err := reader.ReadString('\n')
		if err != nil {
			glog.Fatalf("Unable to read password. Error: %s", err.Error())
		}
		connectionStr = fmt.Sprintf(connectionStr, strings.TrimRight(password, "\n"))
	}

	// Initialize the database.
	db.Init(connectionStr)

	// Get the current database version
	maxDBVersion := db.MaxDBVersion()
	glog.Infof("Latest database version: %d", maxDBVersion)

	dbVersion, err := db.DBVersion()
	if err != nil {
		glog.Fatalf("Unable to retrieve database version. Error: %s", err.Error())
	}
	glog.Infof("Current database version: %d", dbVersion)

	if dbVersion < maxDBVersion {
		glog.Infof("Migrating to version: %d", maxDBVersion)
		err = db.Migrate(maxDBVersion)
		if err != nil {
			glog.Fatalf("Unable to retrieve database version. Error: %s", err.Error())
		}
	}

	glog.Infoln("Database migration finished.")
}
