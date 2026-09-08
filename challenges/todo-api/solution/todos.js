'use strict';

const store = require('./store');

// The most todos one page may return, and what a page returns when the
// request does not say.
const MAX_LIMIT = 100;

function invalid(details) {
	return { status: 400, body: { error: 'invalid_todo', details } };
}

function notFound() {
	return { status: 404, body: { error: 'not_found' } };
}

// checkTitle and checkCompleted are shared by create and update so the two
// cannot drift apart.
function checkTitle(title) {
	if (typeof title !== 'string' || title.trim() === '') {
		return { field: 'title', message: 'title is required and must not be blank' };
	}
	return null;
}

function checkCompleted(completed) {
	if (typeof completed !== 'boolean') {
		return { field: 'completed', message: 'completed must be true or false' };
	}
	return null;
}

function listTodos(query) {
	const limit = readBound(query.get('limit'), MAX_LIMIT);
	const offset = readBound(query.get('offset'), 0);

	if (limit === null || offset === null) {
		return invalid([{ field: 'limit', message: 'limit and offset must be whole numbers of at least 0' }]);
	}

	const page = store.all().slice(offset, offset + limit);
	return { status: 200, body: page };
}

// readBound parses a query parameter that must be a whole number, returning
// fallback when it is absent and null when it is nonsense.
function readBound(raw, fallback) {
	if (raw === null || raw === '') {
		return fallback;
	}

	const value = Number(raw);
	if (!Number.isInteger(value) || value < 0) {
		return null;
	}
	return value;
}

function createTodo(body) {
	const problems = [];

	const titleProblem = checkTitle(body.title);
	if (titleProblem !== null) {
		problems.push(titleProblem);
	}
	if (body.completed !== undefined) {
		const completedProblem = checkCompleted(body.completed);
		if (completedProblem !== null) {
			problems.push(completedProblem);
		}
	}
	if (problems.length > 0) {
		return invalid(problems);
	}

	const todo = store.insert({
		title: body.title.trim(),
		completed: body.completed ?? false,
	});
	return { status: 201, body: todo };
}

function getTodo(id) {
	const todo = store.find(id);
	if (todo === null) {
		return notFound();
	}
	return { status: 200, body: todo };
}

function updateTodo(id, body) {
	const todo = store.find(id);
	if (todo === null) {
		return notFound();
	}

	const problems = [];
	if (body.title !== undefined) {
		const titleProblem = checkTitle(body.title);
		if (titleProblem !== null) {
			problems.push(titleProblem);
		}
	}
	if (body.completed !== undefined) {
		const completedProblem = checkCompleted(body.completed);
		if (completedProblem !== null) {
			problems.push(completedProblem);
		}
	}
	if (problems.length > 0) {
		return invalid(problems);
	}

	// A patch changes the fields it names and leaves the rest alone.
	const updated = { ...todo };
	if (body.title !== undefined) {
		updated.title = body.title.trim();
	}
	if (body.completed !== undefined) {
		updated.completed = body.completed;
	}

	store.save(updated);
	return { status: 200, body: updated };
}

function deleteTodo(id) {
	if (!store.remove(id)) {
		return notFound();
	}
	return { status: 204, body: null };
}

module.exports = { listTodos, createTodo, getTodo, updateTodo, deleteTodo, notFound };
