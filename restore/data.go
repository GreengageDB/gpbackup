package restore

/*
 * This file contains structs and functions related to backing up data on the segments.
 */

import (
	"context"
	"fmt"
	"sort"
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
)

/*
 * errorFile is the error file of the gpbackup_helper that creates the pipe
 * destinationToRead, with --single-data-file or --resize-cluster.
 */
func CopyTableIn(queryContext context.Context, connectionPool *dbconn.DBConn, tableName string, tableAttributes string, destinationToRead string, singleDataFile bool, whichConn int, errorFile string) (int64, error) {
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
		readFromDestinationCommand = fmt.Sprintf("(timeout --foreground 300 bash -c \"while [[ ! -p \"%s\" && ! -f \"%s\" ]]; do sleep 1; done\" || (echo \"Pipe not found %s\">&2; exit 1)) && (test ! -f \"%s\" || (echo \"Error file found %s\">&2; exit 1)) && %s", destinationToRead, errorFile, destinationToRead, errorFile, errorFile, readFromDestinationCommand)
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
			// In a transaction that the failure of the COPY has aborted, the RESET fails,
			// and the rollback of the transaction resets the setting instead
			defer func() {
				_, _ = connectionPool.Exec("RESET gp_enable_segment_copy_checking;", whichConn)
			}()
		}

		/*
		 * An error file means that the restore_helper has encountered an error and
		 * has shut down, so that the COPY would wait for pipes the helper will not
		 * create.  The COPY fails on a segment that finds the error file of its
		 * helper, and a failed COPY is checked for it on all segments.
		 */
		errorFile := utils.GetErrorFilename(fpInfo.GetSegmentPipePathForCopyCommand(helperIdx...))
		partialRowsRestored, copyErr := CopyTableIn(queryContext, connectionPool, tableName, entry.AttributeString, destinationToRead, backupConfig.SingleDataFile, whichConn, errorFile)

		if copyErr != nil {
			gplog.Error(copyErr.Error())
			if backupConfig.SingleDataFile || resizeCluster {
				agentErr := checkAgentErrors(*fpInfo, helperIdx)
				if agentErr != nil {
					gplog.Error(agentErr.Error())
					return agentErr
				}
			}
			if MustGetFlagBool(options.ON_ERROR_CONTINUE) {
				if connectionPool.Version.AtLeast("6") && (backupConfig.SingleDataFile || resizeCluster) {
					// inform segment helpers to skip this entry
					utils.CreateSkipFileOnSegments(entry.Oid, tableName, globalCluster, *fpInfo, helperIdx...)
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

	/*
	 * Rows that are not redistributed are on the wrong segments, so the table is
	 * not restored.  In the transaction of the COPY, its failure is rolled back with
	 * the data of the table.
	 */
	if resizeCluster || entry.DistByEnum {
		// replicated tables cannot be redistributed, so instead expand them if needed
		if entry.IsReplicated && (origSize < destSize) {
			err = ExpandReplicatedTable(origSize, tableName, whichConn)
		} else {
			err = RedistributeTableData(tableName, whichConn)
		}
		if err != nil {
			gplog.Error(err.Error())
			return err
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

	_, _, resizeCluster, batches := GetResizeClusterInfo()
	/*
	 * With --single-data-file, the gpbackup_helper of each data file reads its tables
	 * one after another, and a group of connsPerDataFile connections restores them:
	 * all connections with a single data file, and with several data files as many
	 * as --copy-queue-size gives.
	 */
	numDataFiles := 1
	connsPerDataFile := connectionPool.NumConns
	if backupConfig.SingleDataFile {
		numDataFiles = toc.GetNumDataFiles(dataEntries)
		if numDataFiles > 1 {
			connsPerDataFile = 1
			if FlagChanged(options.COPY_QUEUE_SIZE) && MustGetFlagInt(options.COPY_QUEUE_SIZE) <= connectionPool.NumConns {
				connsPerDataFile = MustGetFlagInt(options.COPY_QUEUE_SIZE)
			}
		}
	}
	groups := NewDataFileGroups(dataEntries, numDataFiles, connectionPool.NumConns, connsPerDataFile)
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

		if backupConfig.SingleDataFile {
			maxHelpers = numDataFiles
		} else if connectionPool.NumConns < totalTables {
			maxHelpers = connectionPool.NumConns
		} else {
			maxHelpers = totalTables
		}

		if backupConfig.SingleDataFile {
			/*
			 * The helper of a data file is started once a group of connections takes
			 * the data file, so that it does not hold the data file open, which a
			 * plugin may be streaming, while the group restores other data files.
			 */
			groups.StartDataFile = func(file int) {
				startRestoreHelper(fpInfo, dataEntries, groups.entriesOfFile[file], file, connsPerDataFile*batches)
			}
		} else {
			// During a larger-to-smaller restore, we need to do multiple passes of
			// data loading so we assign the batches here.
			for whichConn := 0; whichConn < maxHelpers; whichConn++ {
				entryIdxs := make([]int, 0)
				for entryIdx := whichConn; entryIdx < totalTables; entryIdx += maxHelpers {
					entryIdxs = append(entryIdxs, entryIdx)
				}
				if !startRestoreHelper(fpInfo, dataEntries, entryIdxs, whichConn, connectionPool.NumConns*batches) {
					return 0
				}
			}
		}
	}
	/*
	 * With --on-error-continue, the gpbackup_helpers that have stopped: that of each
	 * data file, or with --resize-cluster that of each connection.  Their remaining
	 * tables are reported as errors without being truncated or checked for errors on
	 * the segments again.
	 */
	stoppedHelpers := make([]atomic.Bool, maxHelpers+1)
	/*
	 * A table is truncated in the transaction that restores its data, so that it
	 * keeps its data if they cannot be restored, whether the helper has stopped or
	 * the COPY has failed.  It is truncated with ONLY, so that the transaction locks
	 * no other table.  The TRUNCATE of the parent of an inheritance hierarchy would
	 * lock its children until its data is restored, so that a child earlier in the
	 * same data file would wait for this lock, and the helper for the child, for
	 * good; it would also empty a child restored before.
	 *
	 * A partitioned table, a root or an intermediate level, is truncated on its own
	 * and with its partitions, as before: its TRUNCATE locks all its partitions, and
	 * holding these locks for the whole COPY in several connections at once could
	 * fill the lock table.
	 *
	 * With --single-data-file, the tables of each data file are truncated in the
	 * order of the file, as described at DataFileGroups.
	 */
	truncateTable := MustGetFlagBool(options.INCREMENTAL) || MustGetFlagBool(options.TRUNCATE_TABLE)
	groups.OrderTruncates = truncateTable && backupConfig.SingleDataFile
	partitionedTables := make(map[string]bool)
	if truncateTable {
		partitionedTableFQNs, err := GetPartitionedTableFQNs()
		gplog.FatalOnError(err)
		for _, partitionedTableFQN := range partitionedTableFQNs {
			partitionedTables[partitionedTableFQN] = true
		}
	}
	/*
	 * We break when an interrupt is received and rely on
	 * TerminateHangingCopySessions to stop any COPY
	 * statements in progress if they don't finish on their own.
	 */
	var tableNum int64 = 0
	var workerPool sync.WaitGroup
	var numErrors int32
	var mutex = &sync.Mutex{}
	// Buffered, so that each worker records its panic before it is done
	panicChan := make(chan error, connectionPool.NumConns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Make sure it's called to release resources even if no errors
	groups.WakeWhenDone(ctx)

	for i := 0; i < connectionPool.NumConns; i++ {
		workerPool.Add(1)
		go func(whichConn int) {
			defer workerPool.Done()
			defer func() {
				if panicErr := recover(); panicErr != nil {
					cancel()
					panicChan <- fmt.Errorf("%v", panicErr)
				}
			}()

			setGUCsForConnection(gucStatements, whichConn)
			nextEntryIdx := groups.EntriesOfConn(whichConn)
			for entryIdx, ok := nextEntryIdx(); ok; entryIdx, ok = nextEntryIdx() {
				entry := dataEntries[entryIdx]
				helperIdx := HelperIdx(whichConn)
				if backupConfig.SingleDataFile {
					helperIdx = HelperIdx(entry.DataFileIdx)
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
				helper := 0
				if len(helperIdx) > 0 {
					helper = helperIdx[0]
				}
				if (backupConfig.SingleDataFile || resizeCluster) && stoppedHelpers[helper].Load() {
					groups.TruncateDone(entryIdx)
					gplog.Verbose("Skipping data of table %s, as the gpbackup_helper of its data has stopped", tableName)
					atomic.AddInt32(&numErrors, 1)
					mutex.Lock()
					errorTablesData[tableName] = Empty{}
					mutex.Unlock()
					dataProgressBar.Increment()
					continue
				}
				var err error
				inTransaction := truncateTable && !partitionedTables[tableName]
				if truncateTable {
					if !groups.WaitToTruncate(ctx, entryIdx) {
						return // Error somewhere, terminate
					}
					gplog.Verbose("Truncating table %s prior to restoring data", tableName)
					truncateQuery := `TRUNCATE ` + tableName
					if inTransaction {
						truncateQuery = `TRUNCATE ONLY ` + tableName
						err = connectionPool.Begin(whichConn)
					}
					if err == nil {
						_, err = connectionPool.Exec(truncateQuery, whichConn)
					}
					groups.TruncateDone(entryIdx)
					if err != nil {
						gplog.Error(err.Error())
						// The COPY of the table is not run, so its helpers are told to skip it
						if MustGetFlagBool(options.ON_ERROR_CONTINUE) && connectionPool.Version.AtLeast("6") && (backupConfig.SingleDataFile || resizeCluster) {
							utils.CreateSkipFileOnSegments(entry.Oid, tableName, globalCluster, fpInfo, helperIdx...)
						}
					}
				}
				if err == nil {
					err = restoreSingleTableData(ctx, &fpInfo, entry, tableName, whichConn, helperIdx)
				}
				if inTransaction && connectionPool.Tx[whichConn] != nil {
					if err == nil {
						err = connectionPool.Commit(whichConn)
						if err != nil {
							gplog.Error(err.Error())
						}
					} else {
						// The table keeps the data it had
						_ = connectionPool.Rollback(whichConn)
					}
				}

				if err != nil {
					atomic.AddInt32(&numErrors, 1)
					if errors.Is(err, utils.AgentErr) && MustGetFlagBool(options.ON_ERROR_CONTINUE) {
						// The helper has stopped, but the tables of the other helpers can still be restored
						dataProgressBar.(*pb.ProgressBar).NotPrint = true
						if stoppedHelpers[helper].CompareAndSwap(false, true) {
							gplog.Warn("The gpbackup_helper %d has stopped on a segment, so its tables left are not restored, "+
								"and its helpers on the other segments are told to skip them", helper)
							utils.CreateSkipFilesOnSegments(oidsOfHelper(groups, dataEntries, helper, maxHelpers), globalCluster, fpInfo, helperIdx...)
						}
					} else if !MustGetFlagBool(options.ON_ERROR_CONTINUE) {
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

/*
 * startRestoreHelper starts the gpbackup_helper with the given index, which restores
 * the given data entries, with numPipes of their pipes created ahead.  It returns
 * false if gprestore is being terminated.
 */
func startRestoreHelper(fpInfo filepath.FilePathInfo, dataEntries []toc.CoordinatorDataEntry, entryIdxs []int, whichHelper int, numPipes int) bool {
	origSize, destSize, resizeCluster, batches := GetResizeClusterInfo()
	oidList := make([]string, 0)
	for _, entryIdx := range entryIdxs {
		if dataEntries[entryIdx].IsReplicated {
			oidList = append(oidList, fmt.Sprintf("%d,0", dataEntries[entryIdx].Oid))
			continue
		}

		for b := 0; b < batches; b++ {
			oidList = append(oidList, fmt.Sprintf("%d,%d", dataEntries[entryIdx].Oid, b))
		}
	}

	utils.WriteOidListToSegments(oidList, globalCluster, fpInfo, HelperIdx(whichHelper)...)
	initialPipes := CreateInitialSegmentPipes(oidList, globalCluster, numPipes, fpInfo, HelperIdx(whichHelper)...)
	if wasTerminated.Load() {
		return false
	}
	isFilter := false
	if len(opts.IncludedRelations) > 0 || len(opts.ExcludedRelations) > 0 || len(opts.IncludedSchemas) > 0 || len(opts.ExcludedSchemas) > 0 {
		isFilter = true
	}
	compressStr := ""
	if backupConfig.Compressed {
		compressStr = fmt.Sprintf(" --compression-type %s ", utils.GetPipeThroughProgram().Name)
	}
	// The helper of a data file reads its own data file and segment TOC
	dataFileIdx := 0
	if backupConfig.SingleDataFile {
		dataFileIdx = whichHelper
	}
	utils.StartGpbackupHelpers(globalCluster, fpInfo, "--restore-agent", MustGetFlagString(options.PLUGIN_CONFIG), compressStr, MustGetFlagBool(options.ON_ERROR_CONTINUE), isFilter, &wasTerminated, initialPipes, backupConfig.SingleDataFile, resizeCluster, origSize, destSize, dataFileIdx, gplog.GetVerbosity(), HelperIdx(whichHelper)...)
	return true
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
 * oidsOfHelper returns the oids of the data entries of a gpbackup_helper: those of
 * its data file with --single-data-file, or else those of its connection.
 */
func oidsOfHelper(groups *DataFileGroups, dataEntries []toc.CoordinatorDataEntry, helper int, numHelpers int) []uint32 {
	if backupConfig.SingleDataFile {
		return groups.OidsOfDataFile(dataEntries, helper)
	}
	oids := make([]uint32, 0)
	for entryIdx := helper; entryIdx < len(dataEntries); entryIdx += numHelpers {
		oids = append(oids, dataEntries[entryIdx].Oid)
	}
	return oids
}

/*
 * checkAgentErrors returns an error if the gpbackup_helper with the given index has
 * stopped.  With --single-data-file, several connections may restore the tables of
 * the same helper, so its error file is left for them to find as well.
 */
func checkAgentErrors(fpInfo filepath.FilePathInfo, helperIdx []int) error {
	if backupConfig.SingleDataFile {
		return utils.CheckAgentErrorsOnSegmentsKeepingErrorFile(globalCluster, fpInfo, helperIdx...)
	}
	return utils.CheckAgentErrorsOnSegments(globalCluster, fpInfo, helperIdx...)
}

// The gpbackup_helper of a connection, or with --single-data-file of a data file
func HelperIdx(whichConn int) []int {
	if maxHelpers > 1 {
		return []int{whichConn}
	}
	return []int{}
}

/*
 * DataFileGroups gives each connection the data entries it restores, in order.
 *
 * The gpbackup_helper of a data file reads its tables one after another, so the
 * connections are split into groups of connsPerDataFile, and each data file is
 * restored by one group, which takes its tables in the order of the file,
 * round-robin.  A group that has finished its data file takes the largest data file
 * left, by rows, so that the groups finish at about the same time.  All
 * connections of a group restore the same data files, in the same order.  With a
 * single data file, or without --single-data-file, all connections are one group.
 *
 * A connection never waits for a table further ahead than the pipes its helper has
 * created, as its previous table in the same data file is connsPerDataFile tables
 * earlier.  The earliest table of a data file that is not restored yet belongs to a
 * connection that has not left the data file, so it can always proceed.
 * Connections beyond the last full group restore nothing, as the tables of their
 * part of a group would be left to no one.
 *
 * With OrderTruncates, the tables of each data file are truncated in the order of
 * the file, each one once the tables before it are truncated or skipped.  The
 * transaction of a table holds the lock of its TRUNCATE until its data is
 * restored, while its COPY waits for the helper to restore the tables before it.
 * A connection that truncated its table ahead of them could so hold its lock
 * while another session that has read one of the tables before it waits for this
 * lock, and the TRUNCATE of that table for the session: the database cannot see
 * the wait through the helper, so the restore and the session would wait for
 * good.  Taking the locks in the order of the file, the order in which the helper
 * restores the tables, leaves no such cycle.
 */
type DataFileGroups struct {
	mutex            sync.Mutex
	connsPerDataFile int
	numGroups        int
	entriesOfFile    [][]int
	filesLeft        []int
	filesOfGroup     [][]int
	// Called once for each data file, before its first entry is given
	StartDataFile func(file int)
	started       []sync.Once
	// Whether the tables of each data file are truncated in the order of the file
	OrderTruncates  bool
	fileOfEntry     []int
	positionOfEntry []int
	truncated       [][]bool
	nextToTruncate  []int
	truncateTurn    *sync.Cond
}

func NewDataFileGroups(dataEntries []toc.CoordinatorDataEntry, numDataFiles int, numConns int, connsPerDataFile int) *DataFileGroups {
	groups := &DataFileGroups{
		connsPerDataFile: connsPerDataFile,
		numGroups:        numConns / connsPerDataFile,
		entriesOfFile:    make([][]int, numDataFiles),
		started:          make([]sync.Once, numDataFiles),
		fileOfEntry:      make([]int, len(dataEntries)),
		positionOfEntry:  make([]int, len(dataEntries)),
		truncated:        make([][]bool, numDataFiles),
		nextToTruncate:   make([]int, numDataFiles),
	}
	groups.truncateTurn = sync.NewCond(&groups.mutex)
	groups.filesOfGroup = make([][]int, groups.numGroups)
	rowsOfFile := make([]int64, numDataFiles)
	for entryIdx, entry := range dataEntries {
		groups.fileOfEntry[entryIdx] = entry.DataFileIdx
		groups.positionOfEntry[entryIdx] = len(groups.entriesOfFile[entry.DataFileIdx])
		groups.entriesOfFile[entry.DataFileIdx] = append(groups.entriesOfFile[entry.DataFileIdx], entryIdx)
		groups.truncated[entry.DataFileIdx] = append(groups.truncated[entry.DataFileIdx], false)
		rowsOfFile[entry.DataFileIdx] += entry.RowsCopied
	}
	for file, entries := range groups.entriesOfFile {
		if len(entries) > 0 {
			groups.filesLeft = append(groups.filesLeft, file)
		}
	}
	sort.SliceStable(groups.filesLeft, func(a, b int) bool {
		return rowsOfFile[groups.filesLeft[a]] > rowsOfFile[groups.filesLeft[b]]
	})
	return groups
}

/*
 * WaitToTruncate waits, with OrderTruncates, until the tables before the given data
 * entry in its data file are truncated or skipped.  It returns false if the context
 * is done first.
 */
func (groups *DataFileGroups) WaitToTruncate(ctx context.Context, entryIdx int) bool {
	if !groups.OrderTruncates {
		return true
	}
	file, position := groups.fileOfEntry[entryIdx], groups.positionOfEntry[entryIdx]
	groups.mutex.Lock()
	defer groups.mutex.Unlock()
	for groups.nextToTruncate[file] < position && ctx.Err() == nil {
		groups.truncateTurn.Wait()
	}
	return ctx.Err() == nil
}

// TruncateDone records that the table of a data entry is truncated, or is skipped
func (groups *DataFileGroups) TruncateDone(entryIdx int) {
	if !groups.OrderTruncates {
		return
	}
	file := groups.fileOfEntry[entryIdx]
	groups.mutex.Lock()
	defer groups.mutex.Unlock()
	groups.truncated[file][groups.positionOfEntry[entryIdx]] = true
	for groups.nextToTruncate[file] < len(groups.truncated[file]) && groups.truncated[file][groups.nextToTruncate[file]] {
		groups.nextToTruncate[file]++
	}
	groups.truncateTurn.Broadcast()
}

// WakeWhenDone wakes the connections waiting to truncate their tables once the context is done
func (groups *DataFileGroups) WakeWhenDone(ctx context.Context) {
	if !groups.OrderTruncates {
		return
	}
	go func() {
		<-ctx.Done()
		groups.mutex.Lock()
		defer groups.mutex.Unlock()
		groups.truncateTurn.Broadcast()
	}()
}

// The oids of the data entries of a data file
func (groups *DataFileGroups) OidsOfDataFile(dataEntries []toc.CoordinatorDataEntry, file int) []uint32 {
	oids := make([]uint32, 0, len(groups.entriesOfFile[file]))
	for _, entryIdx := range groups.entriesOfFile[file] {
		oids = append(oids, dataEntries[entryIdx].Oid)
	}
	return oids
}

// The data file a group restores after the given number of others, if one is left
func (groups *DataFileGroups) fileOfGroup(group int, fileNum int) (int, bool) {
	groups.mutex.Lock()
	defer groups.mutex.Unlock()
	if fileNum == len(groups.filesOfGroup[group]) {
		if len(groups.filesLeft) == 0 {
			return 0, false
		}
		groups.filesOfGroup[group] = append(groups.filesOfGroup[group], groups.filesLeft[0])
		groups.filesLeft = groups.filesLeft[1:]
	}
	return groups.filesOfGroup[group][fileNum], true
}

// EntriesOfConn returns a function that gives the next data entry of a connection
func (groups *DataFileGroups) EntriesOfConn(whichConn int) func() (int, bool) {
	group := whichConn / groups.connsPerDataFile
	fileNum := 0
	var entries []int
	next := len(entries)
	done := group >= groups.numGroups
	return func() (int, bool) {
		if done {
			return 0, false
		}
		for next >= len(entries) {
			if entries != nil {
				fileNum++
			}
			file, ok := groups.fileOfGroup(group, fileNum)
			if !ok {
				done = true
				return 0, false
			}
			if groups.StartDataFile != nil {
				groups.started[file].Do(func() { groups.StartDataFile(file) })
			}
			entries = groups.entriesOfFile[file]
			next = whichConn % groups.connsPerDataFile
		}
		entryIdx := entries[next]
		next += groups.connsPerDataFile
		return entryIdx, true
	}
}
