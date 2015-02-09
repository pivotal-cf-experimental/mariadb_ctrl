package mariadb_start_manager

import (
	"fmt"
	"time"

	"github.com/cloudfoundry/mariadb_ctrl/galera_helper"
	. "github.com/cloudfoundry/mariadb_ctrl/logger"
	"github.com/cloudfoundry/mariadb_ctrl/mariadb_helper"
	"github.com/cloudfoundry/mariadb_ctrl/os_helper"
	"github.com/cloudfoundry/mariadb_ctrl/upgrader"
)

const (
	CLUSTERED   = "CLUSTERED"
	SINGLE_NODE = "SINGLE_NODE"

	BOOTSTRAP_COMMAND = "bootstrap"
	JOIN_COMMAND      = "start"
)

type MariaDBStartManager struct {
	osHelper                   os_helper.OsHelper
	stateFileLocation          string
	mysqlClientPath            string
	jobIndex                   int
	numberOfNodes              int
	dbSeedScriptPath           string
	showDatabasesScriptPath    string
	ClusterReachabilityChecker galera_helper.ClusterReachabilityChecker
	maxDatabaseSeedTries       int
	mariaDBHelper              mariadb_helper.DBHelper
	upgrader                   upgrader.Upgrader
	logger                     Logger
}

func New(
	osHelper os_helper.OsHelper,
	mariaDBHelper mariadb_helper.DBHelper,
	upgrader upgrader.Upgrader,
	stateFileLocation string,
	dbSeedScriptPath string,
	jobIndex int,
	numberOfNodes int,
	logger Logger,
	clusterReachabilityChecker galera_helper.ClusterReachabilityChecker,
	maxDatabaseSeedTries int) *MariaDBStartManager {
	return &MariaDBStartManager{
		osHelper:                   osHelper,
		stateFileLocation:          stateFileLocation,
		jobIndex:                   jobIndex,
		numberOfNodes:              numberOfNodes,
		logger:                     logger,
		dbSeedScriptPath:           dbSeedScriptPath,
		ClusterReachabilityChecker: clusterReachabilityChecker,
		maxDatabaseSeedTries:       maxDatabaseSeedTries,
		mariaDBHelper:              mariaDBHelper,
		upgrader:                   upgrader,
	}
}

func (m *MariaDBStartManager) Execute() (err error) {
	needsUpgrade, err := m.upgrader.NeedsUpgrade()
	if err != nil {
		m.logger.Log((fmt.Sprintf("Failed to determine upgrade status with error: %s", err.Error())))
		return
	}
	if needsUpgrade {
		err = m.upgrader.Upgrade()
		if err != nil {
			m.logger.Log((fmt.Sprintf("Failed to upgrade with error: %s", err.Error())))
			return
		}
	}

	// Nodes > 0 always join an existing cluster
	if m.jobIndex != 0 {
		err = m.joinCluster()
		return
	}

	//single-node deploy
	if m.numberOfNodes == 1 {
		m.logger.Log("Single node deploy")
		err = m.bootstrapCluster(SINGLE_NODE)
		return
	}

	//MULTI-NODE DEPLOYMENTS BELOW

	//intial deploy, state file does not exist
	if !m.osHelper.FileExists(m.stateFileLocation) {
		m.logger.Log(fmt.Sprintf("state file does not exist, creating with contents: '%s'", CLUSTERED))
		err = m.bootstrapCluster(CLUSTERED)
		return
	}

	//state file exists
	state, _ := m.osHelper.ReadFile(m.stateFileLocation)
	m.logger.Log(fmt.Sprintf("state file exists and contains: '%s'", state))

	//scaling up from a single node cluster
	if state == SINGLE_NODE {
		err = m.bootstrapCluster(CLUSTERED)
		return
	}

	err = m.node0JoinCluster()
	return
}

func (m *MariaDBStartManager) bootstrapCluster(state string) (err error) {
	err = m.bootstrapNode()
	if err != nil {
		return
	}

	err = m.seedDatabases()
	if err != nil {
		return
	}

	m.logger.Log(fmt.Sprintf("writing file with contents: '%s'", state))
	m.osHelper.WriteStringToFile(m.stateFileLocation, state)
	return
}

func (m *MariaDBStartManager) bootstrapNode() error {
	var command string

	if m.ClusterReachabilityChecker.AnyNodesReachable() {
		command = JOIN_COMMAND
	} else {
		command = BOOTSTRAP_COMMAND
	}
	err := m.mariaDBHelper.StartMysqldInMode(command)
	if err != nil {
		return err
	}
	return nil
}

func (m *MariaDBStartManager) joinCluster() (err error) {
	err = m.mariaDBHelper.StartMysqldInMode(JOIN_COMMAND)
	if err != nil {
		return err
	}

	m.writeStringToFile(CLUSTERED)
	return nil
}

func (m *MariaDBStartManager) writeStringToFile(contents string) {
	m.logger.Log(fmt.Sprintf("updating file with contents: '%s'", contents))
	m.osHelper.WriteStringToFile(m.stateFileLocation, contents)
}

func (m *MariaDBStartManager) node0JoinCluster() (err error) {
	err = m.mariaDBHelper.StartMysqldInMode(JOIN_COMMAND)
	if err != nil {
		return err
	}

	err = m.seedDatabases()
	if err != nil {
		return
	}

	m.writeStringToFile(CLUSTERED)
	return nil
}

func (m *MariaDBStartManager) seedDatabases() (err error) {
	var output string
	for numTries := 0; numTries < m.maxDatabaseSeedTries; numTries++ {
		output, err = m.osHelper.RunCommand("bash", m.dbSeedScriptPath)
		if err == nil {
			m.logger.Log("Seeding databases succeeded.")
			return
		} else {
			m.logger.Log(fmt.Sprintf("There was a problem seeding the database: '%s'", output))
			m.logger.Log("Retrying seeding script...")
			m.osHelper.Sleep(1 * time.Second)
		}
	}

	m.logger.Log(fmt.Sprintf("Error seeding databases: '%s'\n'%s'", err.Error(), output))
	m.mariaDBHelper.StopStandaloneMysql()
	return
}
