/*
   Hockeypuck - OpenPGP key server
   Copyright (C) 2012, 2013  Casey Marshall

   This program is free software: you can redistribute it and/or modify
   it under the terms of the GNU Affero General Public License as published by
   the Free Software Foundation, version 3.

   This program is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU Affero General Public License for more details.

   You should have received a copy of the GNU Affero General Public License
   along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

// hockeypuck is an OpenPGP keyserver.
package main

import (
	"fmt"
	"encoding/hex"
	"github.com/cmars/conflux"
	"github.com/cmars/conflux/recon"
	"launchpad.net/gnuflag"
	. "launchpad.net/hockeypuck"
	"launchpad.net/hockeypuck/openpgp"
	"log"
	"runtime"
	"strings"
)

type pbuildCmd struct {
	configuredCmd
	cache      int
	ignoreDups bool
	nworkers   int
}

func (c *pbuildCmd) Name() string { return "pbuild" }

func (c *pbuildCmd) Desc() string {
	return "Build reconciliation prefix tree from public keys in database"
}

func newPbuildCmd() *pbuildCmd {
	cmd := new(pbuildCmd)
	flags := gnuflag.NewFlagSet(cmd.Name(), gnuflag.ExitOnError)
	flags.StringVar(&cmd.configPath, "config", "", "Hockeypuck configuration file")
	flags.IntVar(&cmd.cache, "cache", 64, "Max diskv cache size (MB)")
	flags.BoolVar(&cmd.ignoreDups, "ignore-dups", false, "Ignore duplicate entries")
	flags.IntVar(&cmd.nworkers, "nworkers", runtime.NumCPU(), "Number of concurrent ptree writers")
	cmd.flags = flags
	return cmd
}

func (c *pbuildCmd) Main() {
	if c.cache <= 0 {
		Usage(c, "Max cache size must be > 0")
	}
	c.configuredCmd.Main()
	InitLog()
	hashes := make(chan *conflux.Zp)
	done := make(chan interface{})
	var db *openpgp.DB
	var err error
	if db, err = openpgp.NewDB(); err != nil {
		die(err)
	}
	var ptree recon.PrefixTree
	reconSettings := recon.NewSettings(openpgp.Config().Settings.TomlTree)
	reconSettings.Set("conflux.recon.diskv.cacheSizeMax", 1024*1024*c.cache)
	if ptree, err = openpgp.NewSksPTree(reconSettings); err != nil {
		die(err)
	}
	if err = ptree.Create(); err != nil {
		panic(err)
	}
	insertPtree := func() {
		defer func() { done <- struct{}{} }()
		n := 0
		for {
			select {
			case z, ok := <-hashes:
				if z != nil {
					err = ptree.Insert(z)
					if err != nil {
						if c.ignoreDups && strings.Contains(err.Error(), "insert duplicate element") {
							continue
						}
						log.Printf("Error inserting %x into ptree: %v", z.Bytes(), err)
						panic(err)
					}
					n++
					if n%1000 == 0 {
						fmt.Printf(".")
					}
				}
				if !ok {
					return
				}
			}
		}
	}
	for i := 0; i < c.nworkers; i++ {
		go insertPtree()
	}
	readHashes(db, hashes)
	close(hashes)
	for i := 0; i < c.nworkers; i++ {
		<-done
	}
	if err = ptree.Flush(); err != nil {
		log.Println("Flush:", err)
	}
}

func readHashes(db *openpgp.DB, hashes chan *conflux.Zp) {
	rows, err := db.DB.Query("SELECT md5 FROM openpgp_pubkey")
	if err != nil {
		die(err)
	}
	for rows.Next() {
		var md5str string
		if err = rows.Scan(&md5str); err != nil {
			die(err)
		}
		digest, err := hex.DecodeString(md5str)
		if err != nil {
			log.Println("Bad md5:", md5str)
			continue
		}
		digest = append(digest, byte(0))
		digestZp := conflux.Zb(conflux.P_SKS, digest)
		hashes <- digestZp
	}
	if err = rows.Err(); err != nil {
		log.Println("Error during hash query:", err)
	}
}
