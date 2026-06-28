package admiral

type FleetTask struct {
	ID             string
	TaskID         string
	OperationID    string
	NodeID         string
	InstanceID     string
	Action         TaskAction
	Tier           TierInfo
	Services       []ServiceInfo
	SharedVolumes  []SharedVolumeInfo
	Backup         *BackupTaskInfo
	Restore        *RestoreTaskInfo
	Storage        *StorageConfig
	SetupCompleted bool
}

type TaskAction string

const (
	ActionProvisionApp   TaskAction = "provision"
	ActionDeprovisionApp TaskAction = "deprovision"
	ActionStartApp       TaskAction = "start"
	ActionStopApp        TaskAction = "stop"
	ActionPauseApp       TaskAction = "pause"
	ActionResumeApp      TaskAction = "resume"
	ActionRestartApp     TaskAction = "restart"
	ActionResizeApp      TaskAction = "resize"
	ActionBackupDatabase  TaskAction = "backup"
	ActionRestoreDatabase TaskAction = "restore"
	ActionBackupVolumes   TaskAction = "backup-volumes"
	ActionInspectApp      TaskAction = "inspect"
	ActionDeleteBackup    TaskAction = "delete-backup"
	ActionTestStorage     TaskAction = "test-storage"
	ActionRestoreBackup   TaskAction = "restore-backup"
	ActionPauseAppStorage TaskAction = "pause-storage"
	ActionReactivateApp   TaskAction = "reactivate"
)

type TierInfo struct {
	Name    string
	CPU     float64
	Memory  string
	Storage string
}

type ServiceInfo struct {
	Name                 string
	Image                string
	Command              string
	Port                 int
	Env                  map[string]string
	Secrets              map[string]string
	DependsOn            []string
	Requires             []string
	Volume               string
	SharedVolumes        []ServiceSharedVolumeMount
	HealthCheck          *YAMLHealthCheck
	User                 string
	Registry             *RegistryInfo
	SetupCommand         string
	SetupTimeout         int
	HealthCheckWaitSecs  int
}

type RegistryInfo struct {
	URL      string
	Server   string
	Username string
	Password string
}

type SharedVolumeInfo struct {
	Name     string
	Mount    string
	Services []string
}

type ServiceSharedVolumeMount struct {
	Name  string
	Mount string
}

type YAMLHealthCheck struct {
	Type             string
	Port             int
	IntervalSeconds  int
	TimeoutSeconds   int
	FailureThreshold int
	Command          []string
	Path             string
	ExpectedStatus   int
}

type BackupTaskInfo struct {
	ID           string
	Service      string
	DatabaseType string
	DatabaseEnv  string
	UsernameEnv  string
	PasswordEnv  string
}

type RestoreTaskInfo struct {
	ID              string
	BackupID        string
	DatabaseType    string
	StorageKey      string
	StorageBackend  string
	VerifyChecksum  bool
	ChecksumSHA256 string
	BackupType      string
	Service         string
}

type TaskResult struct {
	TaskID      string
	OperationID string
	NodeID      string
	Status      string
	Error       string
	Success     bool
	Logs        string
	Metadata    string
}

type StorageReport struct {
	Total             uint64
	Used              uint64
	Available         uint64
	InstanceID        string
	NodeID            string
	StorageState      StorageState
	StorageMessage    string
	CheckedAt         string
	StorageLimitBytes int64
	StorageUsedBytes  int64
	StorageUsedPct    float64
	StorageLimit      string
	StorageUsed       string
}

type StorageState string

const (
	StorageUnknown  StorageState = "unknown"
	StorageOK       StorageState = "ok"
	StorageWarning  StorageState = "warning"
	StorageCritical StorageState = "critical"
	StorageOverQuota StorageState = "over-quota"
)

type CommandStatus struct {
	Running bool
}

const (
	CommandPending    = "pending"
	CommandLeased     = "leased"
	CommandRunning    = "running"
	CommandSucceeded  = "succeeded"
	CommandFailed     = "failed"
	CommandDeadLetter = "dead-letter"
)

type StorageConfig struct {
	Endpoint        string
	Region          string
	Bucket          string
	Prefix          string
	ForcePathStyle  bool
	AccessKeyEnv    string
	SecretKeyEnv    string
	SessionTokenEnv string
	BackupID        string
	Backend         string
	Key             string
	S3 struct {
		Bucket string
		Region string
	}
}

type HeartbeatRequest struct {
	NodeID        string
	Hostname      string
	IP            string
	PodmanVersion string
	FleetVersion  string
	Status        string
	DiskTotal     int64
	DiskUsed      int64
	RAMTotal      int64
	RAMUsed       int64
	RAMAvailable  int64
	PodsActive    int
	PodsPaused    int
	PodsFailed    int
}

type BackupInfo = BackupTaskInfo
type RestoreInfo = RestoreTaskInfo
