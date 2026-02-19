.PHONY: build-TransactionsFunction build test-email clean %

TEMPLATE          := template.yml
SAM_BUILT_TEMPLATE := .aws-sam/build/template.yaml
REGION            := ca-central-1

build-TransactionsFunction:
	CGO_ENABLED=0 GOARCH=arm64 GOOS=linux go build -o $(ARTIFACTS_DIR)/bootstrap ./cmd/lambda

build:
	sam build -t $(TEMPLATE)

test: build
	$(eval S3_KEY := $(filter-out $@,$(MAKECMDGOALS)))
	@test -n "$(S3_KEY)" || (echo "Usage: make test <s3-file-key>"; exit 1)
	sed 's/S3_KEY/$(S3_KEY)/' events/email.json | \
		sam local invoke TransactionsFunction -t $(SAM_BUILT_TEMPLATE) -e - --region $(REGION)
%:
	@:

clean:
	rm -rf .aws-sam
