package main

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-oci8"
)

func main() {
	db, err := sql.Open("oci8", "C##demouser/hemligt@oracledb:1521/ORCLCDB.localdomain")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer db.Close()

	rows, err := db.Query("select * from dept")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var name string
		var location string

		rows.Scan(&id, &name, &location)

		fmt.Println(id, name, location)
	}

}
