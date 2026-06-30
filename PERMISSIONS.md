Entities
- Public any users logged in or not
- LoggedIn all users that are logged in
- Team:team a set of users in a named group.
- User:username a specifc user

Resources
Global all resources of any kind
Exhibition a specific exhibition resource
Gallery a collection of displays; belongs to an Exhibition
Display a presentation layout with slots that reference photos; belongs to a Gallery
Photo an individual photo; belongs to an Exhibition (not to a Gallery or Display)
Public Photo an individual photo with the public flag set
Gallery Group a set of Galleries
Display Group a set of Displays 
Display Group a set of Displays 
Photo Group a set of Photos
Public Photo Group a Photo Group containing only public Photos

Permissions
Permissions grant to an Entity the right to do specic actions on a resource
Labels
	View 
	Create
	Modify
	Delete
Label Names
	View
	Create
	Modify
	Delete
Emoji
	View
	Create
	Modify
	Delete
Comments
	View
	Create
	Create Reply
	Modify
	Delete
Photo
	View
	Create		-- Upload a photo
	Modify		-- Implies all permisions for Labels, Emoji, and Comments for a photo
	Delete		-- Delete a photo
Display
	View
	Create
	Modify		-- Modify the  
	Delete	
Gallery
	View
	Create
	Modify		-- add and remove displays, change 
	Delete
Placard
Cartel			-- descriptive information about a photo
Artifact Label
Object Label
Wall Label
Display Label
Placard
Plinth

Ownership hierarchy
Exhibition -> Gallery -> Display     (Displays belong to Galleries, Galleries belong to Exhibitions)
Exhibition -> Photo                  (Photos belong directly to Exhibitions, not to Galleries or Displays)
Photo -> Labels, Emojis, Comments    (Annotations belong to Photos)

Displays reference Photos via slots but do not own them.

Photo's have
- Title
- Description
- Labels
- Emojis
- Comments
- Accession information
- Accession log

Display's have
- Template
- Slots

Display Slot's have
- Rich Text
- Photo
- Placard

Display Template
- Slot locations
- Slot types
- Placard format
- Placard Item
  - Position in Placard
  - Source
  - Font, weight, size, color
  - Overflow handling

Gallery's have
- Title
- Displays

Exhibition's have
- Photos Archives
- Galleries
