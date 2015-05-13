package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"

	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry/mariadb_ctrl/cluster_health_checker"
	"github.com/cloudfoundry/mariadb_ctrl/mariadb_helper"
	"github.com/cloudfoundry/mariadb_ctrl/os_helper"
	"github.com/cloudfoundry/mariadb_ctrl/start_manager"
	"github.com/cloudfoundry/mariadb_ctrl/upgrader"
	"github.com/pivotal-cf-experimental/service-config"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
)

type Config struct {
	LogFileLocation string
	PidFile         string
	Db              mariadb_helper.Config
	Manager         start_manager.Config
	Upgrader        upgrader.Config
}

func main() {
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	serviceConfig := service_config.New()
	serviceConfig.AddFlags(flags)

	serviceConfig.AddDefaults(Config{
		Db: mariadb_helper.Config{
			User: "root",
		},
		Manager: start_manager.Config{
			MaxDatabaseSeedTries: 1,
		},
	})
	cf_lager.AddFlags(flags)
	flags.Parse(os.Args[1:])

	logger, _ := cf_lager.New("mariadb_ctrl")

	var config Config
	err := serviceConfig.Read(&config)
	if err != nil {
		logger.Fatal("Error reading config file", err)
	}

	osHelper := os_helper.NewImpl()

	mariaDBHelper := mariadb_helper.NewMariaDBHelper(
		osHelper,
		config.Db,
		config.LogFileLocation,
		logger,
	)

	upgrader := upgrader.NewUpgrader(
		osHelper,
		config.Upgrader,
		logger,
		mariaDBHelper,
	)

	galeraHelper := cluster_health_checker.NewClusterHealthChecker(
		config.Manager.ClusterIps,
		logger,
	)

	mgr := start_manager.New(
		osHelper,
		config.Manager,
		mariaDBHelper,
		upgrader,
		logger,
		galeraHelper,
	)

	members := grouper.Members{
		{
			Name:   "start_manager",
			Runner: start_manager.NewRunner(mgr, logger),
		},
	}
	groupRunner := grouper.NewParallel(os.Kill, members)
	process := ifrit.Background(groupRunner)

	select {
	case err = <-process.Wait():
		logger.Fatal("Error starting mariadb", err)
	case <-process.Ready():
		//continue
	}

	err = writePidFile(config, logger)
	if err != nil {
		process.Signal(os.Kill)
		<-process.Wait()

		logger.Fatal("Error writing pidfile", err, lager.Data{
			"PidFile": config.PidFile,
		})
	}

	logger.Info("mariadb_ctrl started")

	err = <-process.Wait()
	if err != nil {
		//TODO: remove pidfile
		logger.Fatal("Error starting mariadb_ctrl", err)
	}
}

func writePidFile(config Config, logger lager.Logger) error {
	logger.Info(fmt.Sprintf("Writing pid to %s", config.PidFile))
	return ioutil.WriteFile(config.PidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
}
