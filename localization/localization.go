package localization

import (
	"github.com/nicksnyder/go-i18n/i18n"
	"github.com/readium/readium-lcp-server/config"
)

//func to load diles with translation according to array in config file
//need to run in main.go in server
//err!=nil  means that one of them can't be opened
func InitTranslations() error {
	acceptableLanguages := config.Config.LicenseStatus.Localization.Languages
	localizationPath := config.Config.LicenseStatus.Localization.Folder

	for _, value := range acceptableLanguages {
		err := i18n.LoadTranslationFile(localizationPath + value + ".json")
		return err
	}

	return nil
}

//func to translate message
//acceptLanguage - Accept-Languages from request header (r.Header.Get("Accept-Language"))
func LocalizeMessage(acceptLanguage string, message *string, key string) {
	defaultLanguage := config.Config.LicenseStatus.Localization.DefaultLanguage

	T, _ := i18n.Tfunc(acceptLanguage, defaultLanguage)
	*message = T(key)
}
