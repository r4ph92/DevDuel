'use strict';

// The hidden tests. They reach the API over HTTP and nothing else: this file
// never imports the player's code, and runs in a different container from it.

const target = process.env.DEVDUEL_TARGET;
const healthUrl = process.env.DEVDUEL_HEALTH_URL;

const MARKER = '##DEVDUEL_RESULTS##';
const SCHEMA = 'devduel.results/1';
const UNKNOWN_ID = 99999999;

const tests = [];

function test(key, fn) {
	tests.push({ key, fn });
}

let counter = 0;

// unique keeps every test's data its own, so nothing depends on what another
// test did or on the order they run in.
function unique(prefix) {
	counter += 1;
	return `${prefix}-${counter}-${Math.random().toString(36).slice(2, 8)}`;
}

async function api(method, path, body) {
	const res = await fetch(target + path, {
		method,
		headers: body === undefined ? {} : { 'content-type': 'application/json' },
		body: body === undefined ? undefined : JSON.stringify(body),
	});

	const text = await res.text();
	let parsed = null;
	if (text !== '') {
		try {
			parsed = JSON.parse(text);
		} catch {
			parsed = null;
		}
	}
	return { status: res.status, body: parsed, text };
}

function assert(condition, message) {
	if (!condition) {
		throw new Error(message);
	}
}

function assertStatus(res, want, what) {
	assert(res.status === want, `${what}: expected ${want}, got ${res.status} ${res.text.slice(0, 120)}`);
}

// newTodo creates a todo and returns it, without asserting the status: which
// status a create answers with is its own requirement.
async function newTodo(title) {
	const res = await api('POST', '/todos', { title });
	assert(res.body !== null && typeof res.body === 'object', `creating a todo returned no object: ${res.text.slice(0, 120)}`);
	return res.body;
}

test('health', async () => {
	const res = await api('GET', '/health');
	assertStatus(res, 200, 'GET /health');
	assert(res.body !== null && res.body.status === 'ok', `expected {"status":"ok"}, got ${res.text.slice(0, 120)}`);
});

test('list-todos', async () => {
	const title = unique('list');
	await newTodo(title);

	const res = await api('GET', '/todos');
	assertStatus(res, 200, 'GET /todos');
	assert(Array.isArray(res.body), `expected an array, got ${res.text.slice(0, 120)}`);
	assert(res.body.some((todo) => todo.title === title), 'a todo that was just created is missing from the list');
});

test('list-pagination', async () => {
	await newTodo(unique('page'));
	await newTodo(unique('page'));

	const full = await api('GET', '/todos?limit=100&offset=0');
	assertStatus(full, 200, 'GET /todos?limit=100');
	assert(full.body.length >= 3, `expected at least three todos to page through, got ${full.body.length}`);

	const page = await api('GET', '/todos?limit=2&offset=1');
	assertStatus(page, 200, 'GET /todos?limit=2&offset=1');
	assert(page.body.length === 2, `limit=2 should return 2 todos, got ${page.body.length}`);

	const want = full.body.slice(1, 3);
	assert(
		JSON.stringify(page.body) === JSON.stringify(want),
		`offset=1&limit=2 should be the second and third todos, got ids ${page.body.map((t) => t.id).join(',')} instead of ${want.map((t) => t.id).join(',')}`,
	);
});

test('create-returns-201', async () => {
	const res = await api('POST', '/todos', { title: unique('created') });
	assertStatus(res, 201, 'POST /todos');
});

test('create-returns-todo', async () => {
	const title = unique('shape');
	const todo = await newTodo(title);

	assert(Number.isInteger(todo.id), `the created todo needs a generated whole-number id, got ${JSON.stringify(todo.id)}`);
	assert(todo.title === title, `title should be ${JSON.stringify(title)}, got ${JSON.stringify(todo.title)}`);
	assert(todo.completed === false, `a new todo starts out not completed, got ${JSON.stringify(todo.completed)}`);
});

test('create-requires-title', async () => {
	for (const body of [{}, { title: '' }, { title: '   ' }, { title: 42 }]) {
		const res = await api('POST', '/todos', body);
		assertStatus(res, 400, `POST /todos with ${JSON.stringify(body)}`);
	}
});

test('create-rejects-non-boolean', async () => {
	for (const completed of ['yes', 1, null]) {
		const res = await api('POST', '/todos', { title: unique('coerce'), completed });
		assertStatus(res, 400, `POST /todos with completed ${JSON.stringify(completed)}`);
	}
});

test('get-todo', async () => {
	const created = await newTodo(unique('fetch'));

	const res = await api('GET', `/todos/${created.id}`);
	assertStatus(res, 200, `GET /todos/${created.id}`);
	assert(res.body !== null && res.body.id === created.id, `expected todo ${created.id}, got ${res.text.slice(0, 120)}`);
});

test('get-unknown-returns-404', async () => {
	const res = await api('GET', `/todos/${UNKNOWN_ID}`);
	assertStatus(res, 404, `GET /todos/${UNKNOWN_ID}`);
});

test('patch-merges-fields', async () => {
	const title = unique('merge');
	const created = await newTodo(title);

	const res = await api('PATCH', `/todos/${created.id}`, { completed: true });
	assertStatus(res, 200, `PATCH /todos/${created.id}`);
	assert(res.body.completed === true, `completed should have become true, got ${JSON.stringify(res.body.completed)}`);
	assert(res.body.title === title, `a patch that does not mention the title must leave it alone, got ${JSON.stringify(res.body.title)}`);
});

test('patch-unknown-returns-404', async () => {
	const res = await api('PATCH', `/todos/${UNKNOWN_ID}`, { completed: true });
	assertStatus(res, 404, `PATCH /todos/${UNKNOWN_ID}`);
});

test('delete-todo', async () => {
	const created = await newTodo(unique('gone'));

	const res = await api('DELETE', `/todos/${created.id}`);
	assertStatus(res, 204, `DELETE /todos/${created.id}`);

	// Checked through the list rather than a second GET, so this does not
	// depend on how a missing todo is reported.
	const list = await api('GET', '/todos?limit=100');
	assert(
		Array.isArray(list.body) && !list.body.some((todo) => todo.id === created.id),
		`todo ${created.id} is still listed after being deleted`,
	);
});

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// waitForApp polls the runner's health endpoint. The tester does this itself:
// nothing outside the judge network can reach either container.
async function waitForApp() {
	const deadline = Date.now() + 45000;

	while (Date.now() < deadline) {
		try {
			const res = await fetch(healthUrl);
			if (res.ok) {
				return;
			}
		} catch {
			// Not listening yet.
		}
		await sleep(200);
	}
	throw new Error(`the app never answered ${healthUrl}`);
}

function report(results) {
	console.log(MARKER);
	console.log(JSON.stringify({ schema: SCHEMA, results }));
}

async function main() {
	try {
		await waitForApp();
	} catch (err) {
		// Every requirement is unknown rather than failed: the app never ran,
		// so nothing was actually tested.
		report(tests.map(({ key }) => ({
			key,
			status: 'error',
			duration_ms: 0,
			message: String(err.message),
		})));
		return;
	}

	const results = [];
	for (const { key, fn } of tests) {
		const started = Date.now();
		try {
			await fn();
			results.push({ key, status: 'pass', duration_ms: Date.now() - started, message: '' });
		} catch (err) {
			results.push({
				key,
				status: 'fail',
				duration_ms: Date.now() - started,
				message: String((err && err.message) || err).slice(0, 500),
			});
		}
	}
	report(results);
}

main();
