param([Parameter(Mandatory)][string]$ProductId,[Parameter(Mandatory)][string]$Package)
$ErrorActionPreference = 'Stop'
foreach ($key in @('STORE_TENANT_ID','STORE_CLIENT_ID','STORE_CLIENT_SECRET','STORE_SELLER_ID')) {
  if (-not [Environment]::GetEnvironmentVariable($key)) { throw "Missing environment secret $key" }
}
# Do not let msstore publish discard an owner-edited pending draft.
# Authentication and blob URLs must never be printed or written to receipts.
$token = Invoke-RestMethod -Method Post -Uri "https://login.microsoftonline.com/$env:STORE_TENANT_ID/oauth2/token" -Body @{
  grant_type='client_credentials'; client_id=$env:STORE_CLIENT_ID; client_secret=$env:STORE_CLIENT_SECRET; resource='https://manage.devcenter.microsoft.com'
}
$app = Invoke-RestMethod -Uri "https://manage.devcenter.microsoft.com/v1.0/my/applications/$ProductId" -Headers @{Authorization="Bearer $($token.access_token)"}
if (-not $app.lastPublishedApplicationSubmission.id) { throw 'Complete and publish the first submission in Partner Center before enabling CI updates.' }
if ($app.pendingApplicationSubmission.id) { throw 'A pending Store submission already exists. Preserve it; finish it before running this workflow.' }
& msstore reconfigure --tenantId $env:STORE_TENANT_ID --sellerId $env:STORE_SELLER_ID --clientId $env:STORE_CLIENT_ID --clientSecret $env:STORE_CLIENT_SECRET | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Store CLI authentication failed.' }
& msstore publish . --inputFile $Package --appId $ProductId
if ($LASTEXITCODE -ne 0) { throw 'Store upload/commit failed; inspect Partner Center before retrying.' }
& msstore submission status $ProductId
if ($LASTEXITCODE -ne 0) { throw 'Submission sent, but status lookup failed. Inspect Partner Center.' }
