# Backup and restore

Everything on your server is on one disk, in one machine, in your house. Disks fail, laptops
get dropped, and things get deleted by mistake. A backup is the second copy.

Homebase makes backups you can read without Homebase. If the server dies completely, you
plug the backup disk into any computer, open a folder, and your files are there.

## What you need

**A second disk.** A USB drive is fine. It has to be big enough for what you are keeping,
and it should be one you can put somewhere else — a drawer in another room is better than
the shelf the server is on, and a relative's house is better still.

Set it up under **Storage** first, the same way as any other disk.

Homebase will not let you back up onto a disk an application keeps its files on. A copy on
the same disk protects against deleting a file by accident, and against nothing else: when a
disk fails it takes everything on it. Being told you are covered when you are not is worse
than knowing you are not.

## Making a backup

Open **Backup**, choose the disk, and pick one of two:

**Settings only** takes a few seconds. It saves your accounts, your applications' settings,
and Homebase's record of which disk is which. Not your films or photographs.

**Everything** also copies the files on your storage disks. On a large collection this takes
hours, and the backup will be about as large as the collection — Homebase keeps a full copy
each time rather than anything clever, because a clever backup is one you cannot read
without Homebase.

You can keep using the server while it runs.

## Checking a backup

**Check this backup** reads every file and compares it with what was recorded when the
backup was made. It takes as long as making the backup did.

This is worth doing, and worth doing occasionally rather than once. A disk sitting in a
drawer for a year quietly develops errors; a backup interrupted by a full disk looks
finished until somebody counts the files. Both are found the hard way if nothing looks
first.

If a check fails, make a new backup. If checks keep failing, the disk is wearing out and
should be replaced.

## Getting a file back, without Homebase

This is the part worth reading before you need it.

Plug the backup disk into any computer — Windows, a Mac, another Linux machine. You will
find a folder called `homebase-backups`, and inside it one folder per backup, named by date.

Inside each one:

| Folder | What is in it |
|---|---|
| `apps` | Each application's own files, one folder per application |
| `data` | The folders you chose to back up — your photographs, films, documents |
| `system` | Homebase's own settings. Only useful to Homebase |

Find the file, copy it. That is the whole procedure. There is nothing to install and nothing
to extract.

There is also a `README.txt` in every backup that says the same thing, for whoever is
looking at the disk when you are not there.

## Restoring a whole server

Install Homebase on the new machine, plug the backup disk in, set it up under **Storage**,
and open **Backup**. Your backups will be listed, including which machine each came from.

Choose one and press **See what this would do** first. Homebase will tell you:

- when the backup was taken, and from which machine
- how many files would be written
- **how many files on this server would be replaced**
- whether the backup is still intact
- which applications it can reinstall, and which it cannot

Nothing has happened at this point. Read it, then decide.

### What restoring does and does not do

**It puts back what is in the backup.** Your applications' data, your files, and Homebase's
settings.

**It does not delete anything else.** If this server has things the backup does not know
about, they stay. You can restore last month's backup to get one application back without
losing the three you have added since.

**It does not restore damaged files over good ones.** Anything in the backup that no longer
matches its checksum is skipped and reported, rather than written on top of something that
works.

**It does not reinstall your applications automatically.** It tells you which ones it can,
and you install them — a restore that silently downloads several gigabytes is not one
anybody expected.

**It does not bring back saved application passwords.** You will be asked for those again.

## Things Homebase does not do yet

- **No automatic backups on a schedule.** You have to press the button. This is the next
  thing to build.
- **No encryption.** Anyone holding the backup disk can read everything on it. Keep it
  somewhere you would keep a box of photographs.
- **No backing up to another computer or to the internet.** A disk you plug in, and that is
  all.
- **Every backup is a full copy.** Ten backups of a 200 GB collection need two terabytes.
  Delete the old ones you do not want.

## How often

Whenever you would mind losing what has changed since the last one.

For most people that means: settings after you change something, and everything once a month
or after adding a lot of photographs. The honest answer is that any backup is enormously
better than none, and the most common backup mistake is not making one at all.
