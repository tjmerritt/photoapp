CLAUDE.md
=========

Create a webpage that displays either a single photo along with details about the photo.  Details include Title, Description,
Labels, Emojis, and a tree of comments.  The webpage should use soley html for layout, alpine.js for interactivity and data insertion
(using html templates populated by alpine.js x-for for lists of data), and tailwind for layout formating.  Create a mock API server
for testing the page.  Please point out things you think might be missing in the APIs.

The page should have a menu at the top the the page.  The menu should have a hamburger icon for selecting dropdown menu items.   A site icon image, a search box , a settings gear button, a user button.  There should be only one menu item titles "Photos".  Hitting enter on the search box should show a popup that says searching is not yet implemented.  The settings button should bring up a popup that says no settings yet.  The user button should bring up a popup with a user name and password box, with a login button.  Clicking the login button should show a notice that login is not yet available.

The photo should be the first thing on the page with the tile and description underneath.  To the right of the photo should be
thumnails of related photos if present in the API response.

Below the descriptions should be one or more rows of labels, each label should be in an oval, with the Name inside a colored oval followed by a box with the label value.  Following the labels should be a cirle with a plus.  The should be bring up a popup with two fields, Name and Text and a button Save.  The popup should have an x in the the top right corner which when pressed should close the popup.

Below the label rows place a row of emojis.  Click and hold on an emoji should bring up a popup with a list of users who have added
this emoji.  At the end of the row a circle with a plus sign.  Pressing the plus brings up a popup with an emoji selector.  Clicking on an emoji adds it to the photo.

Below the emojis should be a box to leave a new comment.   Below this box should comments if any from the comment list.  Comments
should show the user thumbnail, username, nd comment time followed by the comment text.  If the comments is long, it should be shorted and more button added to show the remainder of the comment.  Clicking on the more but should always show at least one additional line.  At the bottom of the comment should be a Replies button to show any replies if the replycount is present and greater than zero.  Otherwise a Reply button shouw be present.  When showing replies, there should always be a box to add a reply at the start.

At the botton should be a footer with a short copyright notice and link for terms of service nd privacy policy.

The following data access APIs are provided.  Additonal APIs for adding and updating content with be added at a later time.

GET /api/v1/photo?photoid=<photoid>
{
	"photoid": "<photoid>",
	"image": {
		"url": "<url>"
		"width": 1234,
		"height": 567,
	},
	"title": {
		"text": "<title>",
		"userid": "<userid>",
		"userame": "<username>",
		"canedit": false,
	},
	"description": "<description>",
	"labels": [
		{
			"labelid": "<id1>",
			"name": "<Name1>",
			"value": "<Value1>",
			"userid": "<userid1>",
			"username": "<username1>",
		},
		{
			"labelid": "<id2>",
			"name": "<Name2>",
			"value": "<Value2>",
			"userid": "<userid2>",
			"username": "<username2>",
		},
	],
	"labelsurl": "<url>",		// url for additional labels can be omitted, ommitted no more than in labels.
	"emojis": [
		{
			"emojiid": "<emojiid1>",
			"imageurl": "<url>",
			"count": 1234,
			"users": [
				{
					"id": "<userid1>",
					"name": "<username1>",
					"tn": "<imageurl>",
				},
				{
					"id": "<userid2>",
					"name": "<username2>",
					"tn": "<imageurl>",
				},
			],
			"usersurl": "<url>",		// url for additional users, may be omitted
		},
	],
	"emojisurl": "<url>",		// url for additional emojis, may be omitted
	"related": [
		{
			"photoid": "<id1>",
			"imageurl": "<url1>",		// scaled image
			"clickurl": "<clickurl1>",	// link to photo info
			"width": 1234,
			"height": 567,
		},
		{
			"photoid": "<id2>",
			"imageurl": "<url2>",		// scaled image
			"clickurl": "<clickurl1>",	// link to photo info
			"width": 1234,
			"height": 567,
		},
	],
	"comments": [
		{
			"commentid": "<commentid1>",
			"author": {
				"userid": "<userid>",
				"username": "<username>",
				"tn": "<imageurl>",
			},
			"date": "YYYY-MM-DDTHH:NN:SS.mmm"
			"replycount": 123,
			"comment": "<commenttext>",
			"repliesurl": "<url>",
		},
		{
			"commentid": "<commentid2>",
			"author": {
				"userid": "<userid>",
				"username": "<username>",
				"tn": "<imageurl>",
			},
			"date": "YYYY-MM-DDTHH:NN:SS.mmm"
			"replycount": 123,
			"comment": "<commenttext>",
			"repliesurl": "<url>",
		},
	],
	"commentsurl": "<url>",			// user for fetching additional comments, may be omitted
}
GET /api/v1/labels?photoid=<photoid>&offset=123&limit=10
{
	"photoid": "<photoid>",
	"offset": 123
	"pages": {
		"count": 2,
		"current": 1,
		"first": "<url>",
		"last": "<url>",
		"next": "<url>",
		"prev": "<url>",
	}
	"labels": [
		{
			"labelid": "<id1>",
			"name": "<Name1>",
			"value": "<Value1>",
			"userid": "<userid1>",
			"username": "<username1>",
		},
		{
			"labelid": "<id2>",
			"name": "<Name2>",
			"value": "<Value2>",
			"userid": "<userid2>",
			"username": "<username2>",
		},
	],
}
GET /api/v1/emojis?photoid=<photoid>&offset=123&limit=10
{
	"photoid": "<photoid>",
	"offset": 123
	"pages": {
		"count": 2,
		"current": 1,
		"first": "<url>",
		"last": "<url>",
		"next": "<url>",
		"prev": "<url>",
	}
	"emojis": [
		"emojiid": "<emojiid>",
		"imageurl": "<url>",
		"count": 1234,
		"users": [
			{
				"id": "<userid1>",
				"name": "<username1>",
			},
			{
				"id": "<userid2>",
				"name": "<username2>",
			},
		],
		"usersurl": "<url>",		// url for additional users, may be omitted
	],
},
GET /api/v1/emoji/users?emoji=<emojiid>&offset=123&limit=10
{
	"emojiid": "<emojiid>",
	"offset": 123
	"pages": {
		"count": 2,
		"current": 1,
		"first": "<url>",
		"last": "<url>",
		"next": "<url>",
		"prev": "<url>",
	}
	"users": [
		{
			"id": "<userid1>",
			"name": "<username1>",
			"tn": "<imageurl>",
		},
		{
			"id": "<userid2>",
			"name": "<username2>",
			"tn": "<imageurl>",
		},
	],
},
GET /api/v1/comments?photoid=<photoid>&offset=123&limit=10
{
	"photoid": "<photoid>",
	"parentid": "<parentcommentid>",
	"offset": 123
	"pages": {
		"count": 2,
		"current": 1,
		"first": "<url>",
		"last": "<url>",
		"next": "<url>",
		"prev": "<url>",
	}
	"comments: [
		{
			"commentid": "<commentid1>",
			"author": {
				"userid": "<userid>",
				"username": "<username>",
			},
			"date": "YYYY-MM-DDTHH:NN:SS.mmm",
			"replycount": 123,
			"comment": "<commenttext>",
			"repliesurl": "<url>",
		},
		{
			"commentid": "<commentid2>",
			"author": {
				"userid": "<userid>",
				"username": "<username>",
			},
			"date": "YYYY-MM-DDTHH:NN:SS.mmm",
			"replycount": 123,
			"comment": "<commenttext>",
			"repliesurl": "<url>",
		},
	],
}

GET /api/v1/user?userid=<userid>
{
	"userid": "<userid>",
	"username": "<username>",
	"profile": {
		"fullname": "<fullname>",
		"joined": "YYYY-MM-DDTHH:NN:SS.mmm",
		"link": "<profileurl>",
		"image": "<imageurl>",
	}
}
