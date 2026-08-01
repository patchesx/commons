// Package permissions defines string constants for all permission keys.
// Import this package instead of using string literals to get compile-time
// checking and a single source of truth for key names.
package permissions

const (
	RecordingsConfigRead    = "recordings.config.read"
	RecordingsConfigWrite   = "recordings.config.write"
	UploadsConfigRead       = "uploads.config.read"
	UploadsConfigWrite      = "uploads.config.write"
	JobsView                = "jobs.view"
	VideosEdit              = "videos.edit"
	ChannelsApproveRequests = "channels.approve_requests"
	ChannelsRequestAccess   = "channels.request_access"
	ResourcesView           = "resources.view"
	MembersView             = "members.view"
	LegislationView         = "legislation.view"
	LegislationManage       = "legislation.manage"
	CalendarView            = "calendar.view"
	CalendarManage          = "calendar.manage"
	QuickLinksView          = "quick_links.view"
	ResourcesManage         = "resources.manage"
	MeetingsView            = "meetings.view"
	MeetingsSchedule        = "meetings.schedule"
	MeetingsManage          = "meetings.manage"
	WorkItemsCreate         = "work_items.create"
	LibraryView             = "library.view"
	LibraryManage           = "library.manage"
	AdminAccess             = "admin.access"
)
