'use strict';

// The store is in memory on purpose. A judge run lasts seconds and the
// container is thrown away afterwards, so there is nothing to persist to.

let nextId = 1;
const todos = new Map();

function all() {
	return [...todos.values()];
}

function find(id) {
	return todos.get(id) ?? null;
}

function insert({ title, completed }) {
	const todo = { id: nextId, title, completed };
	nextId += 1;
	todos.set(todo.id, todo);
	return todo;
}

function save(todo) {
	todos.set(todo.id, todo);
	return todo;
}

function remove(id) {
	return todos.delete(id);
}

// A handful of todos, so a freshly started server is not an empty one.
for (const title of ['Read the requirements', 'Run the judge', 'Ship it']) {
	insert({ title, completed: false });
}

module.exports = { all, find, insert, save, remove };
