class AllSetting {

    constructor(data) {
        this.webListen = "";
        this.webDomain = "";
        this.webPort = 54321;
        this.webCertFile = "";
        this.webKeyFile = "";
        this.webBasePath = "/";
        this.sessionMaxAge = 180;
        this.pageSize = 50;
        this.expireDiff = "";
        this.trafficDiff = "";
        this.remarkModel = "-ieo";
        this.outboundTestUrl = "https://www.gstatic.com/generate_204";
        this.tgBotEnable = false;
        this.tgBotToken = "";
        this.tgBotChatId = "";
        this.tgRunTime = "@daily";
        this.tgBotBackup = false;
        this.tgBotLoginNotify = false;
        this.tgCpu = "";
        this.tgLang = "zh-Hans";
        this.subEnable = false;
        this.subListen = "";
        this.subPort = "2096";
        this.subPath = "/sub/";
        this.subJsonPath = "/json/";
        this.subDomain = "";
        this.subCertFile = "";
        this.subKeyFile = "";
        this.subUpdates = 0;
        this.subEncrypt = true;
        this.subShowInfo = false;
        this.subURI = "";
        this.subJsonURI = "";
        this.subJsonMux = "";
        this.subJsonRules = "";
        this.ipBlockAfterRemove = true;
        this.backupEnable = false;
        this.backupTime = "05:00";
        this.backupFrequency = "daily";
        this.backupRetention = 3;
        this.backupType = "local";
        this.backupDir = "/XUI-GO/Backup";
        this.backupWebdavUrl = "";
        this.backupWebdavUser = "";
        this.backupWebdavPass = "";
        this.backupFtpHost = "";
        this.backupFtpPort = 21;
        this.backupFtpUser = "";
        this.backupFtpPass = "";
        this.backupFtpDir = "/";
        this.backupS3Endpoint = "";
        this.backupS3Region = "us-east-1";
        this.backupS3Bucket = "";
        this.backupS3AccessKey = "";
        this.backupS3SecretKey = "";
        this.backupS3Prefix = "";

        this.timeLocation = "Asia/Hong_Kong";

        if (data == null) {
            return
        }
        ObjectUtil.cloneProps(this, data);
    }

    equals(other) {
        return ObjectUtil.equals(this, other);
    }
}