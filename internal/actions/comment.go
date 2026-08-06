package actions

const powerCommentJobIDLimit = 20

func ClassicPowerComment(jobID string) string {
	return "DoneThen job " + powerCommentJobID(jobID) + " completed"
}

func PluginPowerComment(jobID string) string {
	return "DoneThen plugin job " + powerCommentJobID(jobID) + " completed"
}

func AfterStopPowerComment(jobID string) string {
	return "DoneThen job " + powerCommentJobID(jobID) + ": Codex stopped"
}

func IsFixedPowerComment(jobID, comment string) bool {
	return comment == ClassicPowerComment(jobID) ||
		comment == PluginPowerComment(jobID) ||
		comment == AfterStopPowerComment(jobID)
}

func powerCommentJobID(jobID string) string {
	if len(jobID) <= powerCommentJobIDLimit {
		return jobID
	}
	return jobID[:powerCommentJobIDLimit]
}
