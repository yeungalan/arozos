/*
	Cine Studio - Server render progress

	Returns the task state and conversion progress for a server side
	timeline render started by backend/render.js.

	Parameters:
	  id - the render task ID

	Returns { status, error, output, progress } where progress is the
	JSON written by the Go ffmpeg progress monitor (or null before the
	first update).
*/

requirelib("filelib");

function main() {
	if (typeof(id) == "undefined" || id == "" || !id.match(/^[A-Za-z0-9_\-]+$/)) {
		sendJSONResp(JSON.stringify({ error: "missing or invalid id" }));
		return;
	}

	var taskFile = "tmp:/cinestudio/" + id + ".task.json";
	var progressFile = "tmp:/cinestudio/" + id + ".progress.json";

	if (!filelib.fileExists(taskFile)) {
		sendJSONResp(JSON.stringify({ error: "not_found" }));
		return;
	}

	var task = {};
	try {
		task = JSON.parse(filelib.readFile(taskFile));
	} catch (e) {
		sendJSONResp(JSON.stringify({ error: "task file unreadable" }));
		return;
	}

	var progress = null;
	if (filelib.fileExists(progressFile)) {
		try {
			progress = JSON.parse(filelib.readFile(progressFile));
		} catch (e) {
			progress = null;
		}
	}

	sendJSONResp(JSON.stringify({
		status: task.status,
		error: task.error,
		output: task.output,
		progress: progress
	}));
}

main();
