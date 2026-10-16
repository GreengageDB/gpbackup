package restore

/*
 * This file contains structs and functions related to backing up data on the segments.
 */

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/GreengageDB/gp-common-go-libs/cluster"
	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gpbackup/filepath"
	"github.com/GreengageDB/gpbackup/options"
	"github.com/GreengageDB/gpbackup/toc"
	"github.com/GreengageDB/gpbackup/utils"
	"github.com/jackc/pgconn"
	"github.com/pkg/errors"
	"gopkg.in/cheggaaa/pb.v1"
)

var (
	tableDelim = ","
	maxHelpers int

	/*
	 * A backup taken with --single-data-file and --jobs has several data files per
	 * segment, each read by its own gpbackup_helper, whose index is the data file of
	 * a data entry.
	 *
	 * Several connections may restore the tables of one data file, so failedDataFiles
	 * records the data files whose gpbackup_helper has stopped: with
	 * --on-error-continue every connection then skips their tables instead of
	 * waiting for pipes that the helper will not create.  It is nil for other backups.
	 */
	failedDataFiles []atomic.Bool
	dataFileMutexes []sync.Mutex
)

func CopyTableIn(queryContext context.Context, connectionPool *dbconn.DBConn, tableName string, tableAttributes string, destinationToRead string, singleDataFile bool, whichConn int, helperIdx ...int) (int64, error) {
	if wasTerminated.Load() {
		return -1, nil
	}
	whichConn = connectionPool.ValidateConnNum(whichConn)
	copyCommand := ""
	readFromDestinationCommand := "cat"
	customPipeThroughCommand := utils.GetPipeThroughProgram().InputCommand
	resizeCluster := MustGetFlagBool(options.RESIZE_CLUSTER)

	if singleDataFile || resizeCluster {
		//helper.go handles compression, so we don't want to set it here
		customPipeThroughCommand = utils.DefaultPipeThroughProgram
		errorFile := utils.GetErrorFilename(globalFPInfo.GetSegmentPipePathForCopyCommand(helperIdx...))
		readFromDestinationCommand = fmt.Sprintf("(timeout --foreground 300 bash -c \"while [[ ! -p \"%s\" && ! -f \"%s\" ]]; do sleep 1; done\" || (echo \"Pipe not found %s\">&2; exit 1)) && %s", destinationToRead, errorFile, destinationToRead, readFromDestinationCommand)
	} else if MustGetFlagString(options.PLUGIN_CONFIG) != "" {
		readFromDestinationCommand = fmt.Sprintf("%s restore_data %s", pluginConfig.ExecutablePath, pluginConfig.ConfigPath)
	}

	if customPipeThroughCommand == utils.DefaultPipeThroughProgram {
		copyCommand = fmt.Sprintf("PROGRAM '%s %s'", readFromDestinationCommand, destinationToRead)
	} else {
		copyCommand = fmt.Sprintf("PROGRAM '%s %s | %s'", readFromDestinationCommand, destinationToRead, customPipeThroughCommand)
	}

	query := fmt.Sprintf("COPY %s%s FROM %s WITH CSV DELIMITER '%s' ON SEGMENT;", tableName, tableAttributes, copyCommand, tableDelim)

	if connectionPool.Version.AtLeast("7") {
		utils.LogProgress(`Executing "%s" on coordinator`, query)
	} else {
		utils.LogProgress(`Executing "%s" on master`, query)
	}
	result, err := connectionPool.ExecContext(queryContext, query, whichConn)
	if err != nil {
		errStr := fmt.Sprintf("Error loading data into table %s", tableName)

		// The COPY ON SEGMENT error might contain useful CONTEXT output
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Where != "" {
			errStr = fmt.Sprintf("%s: %s", errStr, pgErr.Where)
		}

		err = errors.Wrap(err, errStr)

		return 0, err
	}

	rowsLoaded, _ := result.RowsAffected()

	return rowsLoaded, nil
}

func restoreSingleTableData(queryContext context.Context, fpInfo *filepath.FilePathInfo, entry toc.CoordinatorDataEntry, tableName string, whichConn int, helperIdx []int) error {
	origSize, destSize, resizeCluster, batches := GetResizeClusterInfo()

	var numRowsRestored int64
	// We don't want duplicate data for replicated tables so only do one batch
	if entry.IsReplicated {
		batches = 1
	}
	for i := 0; i < batches; i++ {
		destinationToRead := ""
		if backupConfig.SingleDataFile || resizeCluster {
			destinationToRead = fmt.Sprintf("%s_%d_%d", fpInfo.GetSegmentPipePathForCopyCommand(helperIdx...), entry.Oid, i)
		} else {
			destinationToRead = fpInfo.GetTableBackupFilePathForCopyCommand(entry.Oid, utils.GetPipeThroughProgram().Extension, backupConfig.SingleDataFile)
		}
		gplog.Debug("Reading from %s", destinationToRead)

		if entry.DistByEnum {
			gplog.Verbose("Setting gp_enable_segment_copy_checking TO off for table %s", tableName)
			connectionPool.MustExec("SET gp_enable_segment_copy_checking TO off;", whichConn)
			defer connectionPool.MustExec("RESET gp_enable_segment_copy_checking;", whichConn)
		}

		// In the case where an error file is found, this means that the
		// restore_helper has encountered an error and has shutdown.
		// If this occurs we need to error out, as subsequent COPY statements
		// will hang indefinitely waiting to read from pipes that the helper
		// was expected to set up
		if backupConfig.SingleDataFile || resizeCluster {
			agentErr := CheckAgentErrors(helperIdx...)
			if agentErr != nil {
				gplog.Error(agentErr.Error())
				return agentErr
			}
		}

		partialRowsRestored, copyErr := CopyTableIn(queryContext, connectionPool, tableName, entry.AttributeString, destinationToRead, backupConfig.SingleDataFile, whichConn, helperIdx...)

		if copyErr != nil {
			gplog.Error(copyErr.Error())
			if MustGetFlagBool(options.ON_ERROR_CONTINUE) {
				if connectionPool.Version.AtLeast("6") && (backupConfig.SingleDataFile || resizeCluster) {
					// inform segment helpers to skip this entry
					utils.CreateSkipFileOnSegments(entry.Oid, tableName, globalCluster, globalFPInfo, helperIdx...)
				}
			}
			return copyErr
		}
		numRowsRestored += partialRowsRestored
	}

	numRowsBackedUp := entry.RowsCopied

	// For replicated tables, we don't restore second and subsequent batches of data in the larger-to-smaller case,
	// as that would duplicate data, so we have to "scale down" the values to determine whether the correct number
	// of rows was restored
	if entry.IsReplicated && origSize > destSize {
		numRowsBackedUp /= int64(origSize)
		numRowsRestored /= int64(destSize)
	}

	err := CheckRowsRestored(numRowsRestored, numRowsBackedUp, tableName)
	if err != nil {
		gplog.Error(err.Error())
		return err
	}

	if resizeCluster || entry.DistByEnum {
		// replicated tables cannot be redistributed, so instead expand them if needed
		if entry.IsReplicated && (origSize < destSize) {
			err := ExpandReplicatedTable(origSize, tableName, whichConn)
			if err != nil {
				gplog.Error(err.Error())
			}
		} else {
			err := RedistributeTableData(tableName, whichConn)
			if err != nil {
				gplog.Error(err.Error())
			}
		}
	}

	return nil
}

func ExpandReplicatedTable(origSize int, tableName string, whichConn int) error {
	// Replicated tables will only be initially restored to the segments backup was run from, and
	// redistributing does not cause the data to be replicated to the new segments.
	// To work around this, update the distribution policy entry for those tables to the original cluster size
	// and then explicitly expand them to cause the data to be replicated to all new segments.
	gplog.Debug("Distributing replicated data for %s", tableName)
	alterDistPolQuery := fmt.Sprintf("UPDATE gp_distribution_policy SET numsegments=%d WHERE localoid = '%s'::regclass::oid", origSize, utils.EscapeSingleQuotes(tableName))
	_, err := connectionPool.Exec(alterDistPolQuery, whichConn)
	if err != nil {
		return err
	}

	expandTableQuery := fmt.Sprintf("ALTER TABLE %s EXPAND TABLE;", tableName)
	_, err = connectionPool.Exec(expandTableQuery, whichConn)
	if err != nil {
		return err
	}

	return nil
}

func CheckRowsRestored(rowsRestored int64, rowsBackedUp int64, tableName string) error {
	if rowsRestored != rowsBackedUp {
		rowsErrMsg := fmt.Sprintf("Expected to restore %d rows to table %s, but restored %d instead", rowsBackedUp, tableName, rowsRestored)
		return errors.New(rowsErrMsg)
	}
	return nil
}

func RedistributeTableData(tableName string, whichConn int) error {
	gplog.Debug("Redistributing data for %s", tableName)
	query := fmt.Sprintf("ALTER TABLE %s SET WITH (REORGANIZE=true)", tableName)
	_, err := connectionPool.Exec(query, whichConn)
	return err
}

func restoreDataFromTimestamp(fpInfo filepath.FilePathInfo, dataEntries []toc.CoordinatorDataEntry,
	gucStatements []toc.StatementWithType, dataProgressBar utils.ProgressBar) int32 {
	totalTables := len(dataEntries)
	if totalTables == 0 {
		gplog.Verbose("No data to restore for timestamp = %s", fpInfo.Timestamp)
		return 0
	}

	origSize, destSize, resizeCluster, batches := GetResizeClusterInfo()
	numDataFiles := 1
	connsPerDataFile := 1
	if backupConfig.SingleDataFile {
		numDataFiles = toc.GetNumDataFiles(dataEntries)
		if numDataFiles > 1 && FlagChanged(options.COPY_QUEUE_SIZE) && MustGetFlagInt(options.COPY_QUEUE_SIZE) <= connectionPool.NumConns {
			connsPerDataFile = MustGetFlagInt(options.COPY_QUEUE_SIZE)
		}
	}
	if backupConfig.SingleDataFile || resizeCluster {
		msg := ""
		if backupConfig.SingleDataFile {
			msg += "single data file "
		}
		if resizeCluster {
			msg += "resize "
		}
		gplog.Verbose("Initializing pipes and gpbackup_helper on segments for %srestore", msg)
		utils.VerifyHelperVersionOnSegments(version, globalCluster)

		if backupConfig.SingleDataFile && numDataFiles > 1 {
			maxHelpers = numDataFiles
		} else if backupConfig.SingleDataFile {
			maxHelpers = 1
		} else if connectionPool.NumConns < totalTables {
			maxHelpers = connectionPool.NumConns
		} else {
			maxHelpers = totalTables
		}

		for whichConn := 0; whichConn < maxHelpers; whichConn++ {
			// During a larger-to-smaller restore, we need to do multiple passes of
			// data loading so we assign the batches here.
			oidList := make([]string, 0)
			numPipes := connectionPool.NumConns * batches
			entryIdxs := make([]int, 0)
			if numDataFiles > 1 {
				// Helper whichConn reads data file whichConn, which is restored by connsPerDataFile connections
				numPipes = connsPerDataFile * batches
				for entryIdx := 0; entryIdx < totalTables; entryIdx++ {
					if dataEntries[entryIdx].DataFileIdx == whichConn {
						entryIdxs = append(entryIdxs, entryIdx)
					}
				}
				if len(entryIdxs) == 0 {
					continue
				}
			} else {
				for entryIdx := whichConn; entryIdx < totalTables; entryIdx += maxHelpers {
					entryIdxs = append(entryIdxs, entryIdx)
				}
			}
			for _, entryIdx := range entryIdxs {
				if dataEntries[entryIdx].IsReplicated {
					oidList = append(oidList, fmt.Sprintf("%d,0", dataEntries[entryIdx].Oid))
					continue
				}

				for b := 0; b < batches; b++ {
					oidList = append(oidList, fmt.Sprintf("%d,%d", dataEntries[entryIdx].Oid, b))
				}
			}

			utils.WriteOidListToSegments(oidList, globalCluster, fpInfo, HelperIdx(whichConn)...)
			initialPipes := CreateInitialSegmentPipes(oidList, globalCluster, numPipes, fpInfo, HelperIdx(whichConn)...)
			if wasTerminated.Load() {
				return 0
			}
			isFilter := false
			if len(opts.IncludedRelations) > 0 || len(opts.ExcludedRelations) > 0 || len(opts.IncludedSchemas) > 0 || len(opts.ExcludedSchemas) > 0 {
				isFilter = true
			}
			compressStr := ""
			if backupConfig.Compressed {
				compressStr = fmt.Sprintf(" --compression-type %s ", utils.GetPipeThroughProgram().Name)
			}
			dataFileIdx := 0
			if numDataFiles > 1 {
				dataFileIdx = whichConn
			}
			utils.StartGpbackupHelpers(globalCluster, fpInfo, "--restore-agent", MustGetFlagString(options.PLUGIN_CONFIG), compressStr, MustGetFlagBool(options.ON_ERROR_CONTINUE), isFilter, &wasTerminated, initialPipes, backupConfig.SingleDataFile, resizeCluster, origSize, destSize, dataFileIdx, gplog.GetVerbosity(), HelperIdx(whichConn)...)
		}
	}
	SetNumDataFiles(numDataFiles)
	entryIdxsForConn := GetEntryIdxsForConns(dataEntries, numDataFiles, connectionPool.NumConns, connsPerDataFile)
	/*
	 * We break when an interrupt is received and rely on
	 * TerminateHangingCopySessions to stop any COPY
	 * statements in progress if they don't finish on their own.
	 */
	var tableNum int64 = 0
	var workerPool sync.WaitGroup
	var numErrors int32
	var mutex = &sync.Mutex{}
	panicChan := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Make sure it's called to release resources even if no errors

	for i := 0; i < connectionPool.NumConns; i++ {
		workerPool.Add(1)
		go func(whichConn int) {
			defer func() {
				if panicErr := recover(); panicErr != nil {
					cancel()
					panicChan <- fmt.Errorf("%v", panicErr)
				}
			}()
			defer workerPool.Done()

			setGUCsForConnection(gucStatements, whichConn)
			for _, entryIdx := range entryIdxsForConn[whichConn] {
				entry := dataEntries[entryIdx]
				helperIdx := HelperIdx(whichConn)
				if numDataFiles > 1 {
					if failedDataFiles[entry.DataFileIdx].Load() {
						continue
					}
					helperIdx = []int{entry.DataFileIdx}
				}
				// Check if any error occurred in any other goroutines:
				select {
				case <-ctx.Done():
					return // Error somewhere, terminate
				default: // Default is must to avoid blocking
				}
				if wasTerminated.Load() {
					dataProgressBar.(*pb.ProgressBar).NotPrint = true
					cancel()
					return
				}
				tableName := utils.MakeFQN(entry.Schema, entry.Name)
				if opts.RedirectSchema != "" {
					tableName = utils.MakeFQN(opts.RedirectSchema, entry.Name)
				}
				// Truncate table before restore, if needed
				var err error
				if MustGetFlagBool(options.INCREMENTAL) || MustGetFlagBool(options.TRUNCATE_TABLE) {
					gplog.Verbose("Truncating table %s prior to restoring data", tableName)
					_, err := connectionPool.Exec(`TRUNCATE `+tableName, whichConn)
					if err != nil {
						gplog.Error(err.Error())
					}
				}
				if err == nil {
					err = restoreSingleTableData(ctx, &fpInfo, entry, tableName, whichConn, helperIdx)
				}

				if err != nil {
					atomic.AddInt32(&numErrors, 1)
					if errors.Is(err, utils.AgentErr) && MustGetFlagBool(options.ON_ERROR_CONTINUE) && maxHelpers > 1 {
						dataProgressBar.(*pb.ProgressBar).NotPrint = true
						if numDataFiles > 1 {
							// The helper of this data file has stopped, which CheckAgentErrors has recorded
							// for all connections, but the other data files can still be restored
							continue
						}
						return
					}
					if errors.Is(err, utils.AgentErr) || !MustGetFlagBool(options.ON_ERROR_CONTINUE) {
						dataProgressBar.(*pb.ProgressBar).NotPrint = true
						cancel()
						return
					}
					mutex.Lock()
					errorTablesData[tableName] = Empty{}
					mutex.Unlock()
				} else {
					utils.LogProgress("Restored data to table %s from file (table %d of %d)", tableName, atomic.AddInt64(&tableNum, 1), totalTables)
				}

				dataProgressBar.Increment()
			}
		}(i)
	}
	workerPool.Wait()
	// Allow panics to crash from the main process, invoking DoCleanup
	select {
	case err := <-panicChan:
		gplog.Fatal(err, "")
	default:
		// no panic, nothing to do
	}

	if numErrors > 0 {
		fmt.Println("")
		gplog.Error("Encountered %d error(s) during table data restore; see log file %s for a list of table errors.", numErrors, gplog.GetLogFilePath())
	}

	return numErrors
}

func CreateInitialSegmentPipes(oidList []string, c *cluster.Cluster, numConns int, fpInfo filepath.FilePathInfo, helperIdx ...int) int {
	// Create min(connections, tables) segment pipes on each host
	var maxPipes int
	if !backupConfig.SingleDataFile {
		maxPipes = 1 // Create single initial pipe for the first oid in the restore oids list for non --single-data-file data file restore.
	} else if numConns < len(oidList) {
		maxPipes = numConns
	} else {
		maxPipes = len(oidList)
	}
	utils.CreateSegmentPipesOnAllHostsForRestore(oidList[:maxPipes], c, fpInfo, helperIdx...)
	return maxPipes
}

/*
 * CheckAgentErrors returns an error if the gpbackup_helper with the given index has
 * stopped.  CheckAgentErrorsOnSegments removes the error file it finds, so with
 * several data files the first connection to find it records the failure of the
 * data file for the other connections restoring that file.  The check and the
 * record are done under the lock of the data file, so that no connection misses
 * the error between the removal of the file and the record.
 */
func CheckAgentErrors(helperIdx ...int) error {
	if failedDataFiles == nil {
		return utils.CheckAgentErrorsOnSegments(globalCluster, globalFPInfo, helperIdx...)
	}
	dataFile := helperIdx[0]
	dataFileMutexes[dataFile].Lock()
	defer dataFileMutexes[dataFile].Unlock()
	if failedDataFiles[dataFile].Load() {
		return errors.Wrapf(utils.AgentErr, "The gpbackup_helper of data file %d has already stopped", dataFile)
	}
	err := utils.CheckAgentErrorsOnSegments(globalCluster, globalFPInfo, helperIdx...)
	if err != nil {
		failedDataFiles[dataFile].Store(true)
	}
	return err
}

func HelperIdx(whichConn int) []int {
	if maxHelpers > 1 {
		return []int{whichConn}
	}
	return []int{}
}

/*
 * GetEntryIdxsForConns returns the data entries each connection restores, in order.
 *
 * The gpbackup_helper of a data file reads its tables one after another, so with
 * several data files the connections are split into groups of connsPerDataFile.
 * Each data file is restored by one group, which takes its tables in the order of
 * the file, round-robin.  The data files are spread over the groups.  As every
 * connection restores its entries in the order of the data entries, the earliest
 * entry that is not restored yet can always proceed.
 *
 * The round-robin is kept for each data file, not for the whole group: the entry
 * of a connection that comes before the next one in the same data file is then
 * connsPerDataFile entries earlier in that file, so a connection never waits for
 * a table further ahead than the pipes its gpbackup_helper has created.
 *
 * Otherwise the tables are spread round-robin over all connections.
 */
func GetEntryIdxsForConns(dataEntries []toc.CoordinatorDataEntry, numDataFiles int, numConns int, connsPerDataFile int) [][]int {
	entryIdxsForConn := make([][]int, numConns)
	numGroups := numConns / connsPerDataFile
	entriesInFile := make([]int, numDataFiles)
	for entryIdx, entry := range dataEntries {
		whichConn := entryIdx % numConns
		if numDataFiles > 1 {
			group := entry.DataFileIdx % numGroups
			whichConn = group*connsPerDataFile + entriesInFile[entry.DataFileIdx]%connsPerDataFile
			entriesInFile[entry.DataFileIdx]++
		}
		entryIdxsForConn[whichConn] = append(entryIdxsForConn[whichConn], entryIdx)
	}
	return entryIdxsForConn
}
