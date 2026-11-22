// GraphQL documents posted by the Linear actions: one const per upstream
// query/mutation, moved verbatim from the pre-split linear.go.
package collab

const (
	linearIssueCreateMutation = `
	mutation IssueCreate($input: IssueCreateInput!) {
		issueCreate(input: $input) {
			success
			issue {
				id
				identifier
				title
				description
				url
				priority
				state { id name }
				assignee { id name }
				labels { nodes { id name } }
				project { id name }
				createdAt
			}
		}
	}
	`

	linearIssueQuery = `
	query Issue($id: String!) {
		issue(id: $id) {
			id
			identifier
			title
			description
			url
			priority
			priorityLabel
			state { id name color }
			assignee { id name email }
			labels { nodes { id name color } }
			project { id name }
			team { id name key }
			comments { nodes { id body createdAt user { name } } }
			createdAt
			updatedAt
		}
	}
	`

	linearIssueUpdateMutation = `
	mutation IssueUpdate($id: String!, $input: IssueUpdateInput!) {
		issueUpdate(id: $id, input: $input) {
			success
			issue {
				id
				identifier
				title
				description
				url
				priority
				state { id name }
				assignee { id name }
				labels { nodes { id name } }
				updatedAt
			}
		}
	}
	`

	linearIssueDeleteMutation = `
	mutation IssueDelete($id: String!) {
		issueDelete(id: $id) {
			success
		}
	}
	`

	linearIssuesQuery = `
	query Issues($filter: IssueFilter, $first: Int) {
		issues(filter: $filter, first: $first) {
			nodes {
				id
				identifier
				title
				description
				url
				priority
				priorityLabel
				state { id name color }
				assignee { id name }
				labels { nodes { id name } }
				project { id name }
				team { id name key }
				createdAt
				updatedAt
			}
			pageInfo {
				hasNextPage
				endCursor
			}
		}
	}
	`

	linearCommentCreateMutation = `
	mutation CommentCreate($input: CommentCreateInput!) {
		commentCreate(input: $input) {
			success
			comment {
				id
				body
				createdAt
				user { id name }
			}
		}
	}
	`

	linearProjectCreateMutation = `
	mutation ProjectCreate($input: ProjectCreateInput!) {
		projectCreate(input: $input) {
			success
			project {
				id
				name
				description
				url
				state
				progress
				targetDate
				lead { id name }
				teams { nodes { id name } }
				createdAt
			}
		}
	}
	`

	linearProjectQuery = `
	query Project($id: String!) {
		project(id: $id) {
			id
			name
			description
			url
			state
			progress
			targetDate
			lead { id name email }
			teams { nodes { id name key } }
			issues { nodes { id identifier title state { name } } }
			createdAt
			updatedAt
		}
	}
	`

	linearProjectUpdateMutation = `
	mutation ProjectUpdate($id: String!, $input: ProjectUpdateInput!) {
		projectUpdate(id: $id, input: $input) {
			success
			project {
				id
				name
				description
				url
				state
				progress
				targetDate
				updatedAt
			}
		}
	}
	`

	linearProjectsQuery = `
	query Projects($filter: ProjectFilter, $first: Int) {
		projects(filter: $filter, first: $first) {
			nodes {
				id
				name
				description
				url
				state
				progress
				targetDate
				lead { id name }
				teams { nodes { id name } }
			}
			pageInfo {
				hasNextPage
				endCursor
			}
		}
	}
	`

	linearTeamsQuery = `
	query Teams {
		teams {
			nodes {
				id
				name
				key
				description
				private
				timezone
			}
		}
	}
	`

	linearTeamQuery = `
	query Team($id: String!) {
		team(id: $id) {
			id
			name
			key
			description
			private
			timezone
			states { nodes { id name color type position } }
			labels { nodes { id name color } }
			members { nodes { id name email } }
		}
	}
	`

	linearWorkflowStatesQuery = `
	query WorkflowStates($teamId: ID!) {
		workflowStates(filter: { team: { id: { eq: $teamId } } }) {
			nodes {
				id
				name
				color
				type
				position
				description
			}
		}
	}
	`

	linearIssueLabelCreateMutation = `
	mutation IssueLabelCreate($input: IssueLabelCreateInput!) {
		issueLabelCreate(input: $input) {
			success
			issueLabel {
				id
				name
				color
				description
			}
		}
	}
	`

	linearIssueLabelsQuery = `
	query IssueLabels($filter: IssueLabelFilter) {
		issueLabels(filter: $filter) {
			nodes {
				id
				name
				color
				description
				team { id name }
			}
		}
	}
	`

	linearCyclesQuery = `
	query Cycles($filter: CycleFilter, $first: Int) {
		cycles(filter: $filter, first: $first) {
			nodes {
				id
				number
				name
				startsAt
				endsAt
				completedAt
				progress
				scopeHistory
				issueCountHistory
			}
			pageInfo {
				hasNextPage
				endCursor
			}
		}
	}
	`

	linearIssueCommentsQuery = `
	query Issue($id: String!) {
		issue(id: $id) {
			comments(first: 50) {
				nodes {
					id
					body
					createdAt
					updatedAt
					user { id name email }
				}
			}
		}
	}
	`

	linearIssueRelationCreateMutation = `
	mutation IssueRelationCreate($input: IssueRelationCreateInput!) {
		issueRelationCreate(input: $input) {
			success
			issueRelation {
				id
				type
				issue { id identifier title }
				relatedIssue { id identifier title }
			}
		}
	}
	`

	linearUsersQuery = `
	query Users {
		users {
			nodes {
				id
				name
				displayName
				email
				active
				admin
				avatarUrl
			}
		}
	}
	`

	linearUserQuery = `
	query User($id: String!) {
		user(id: $id) {
			id
			name
			displayName
			email
			active
			admin
			avatarUrl
			assignedIssues {
				nodes {
					id
					identifier
					title
					state { name }
				}
			}
		}
	}
	`

	linearViewerQuery = `
	query Viewer {
		viewer {
			id
			name
			displayName
			email
			active
			admin
			avatarUrl
			assignedIssues {
				nodes {
					id
					identifier
					title
					state { name }
					priority
				}
			}
		}
	}
	`
)
